package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/madshermansen/terraform-provider-nanelo/internal/client"
	"github.com/madshermansen/terraform-provider-nanelo/internal/fakenanelo"
)

// testEnv runs provider tests against an in-memory fake API by default. With TF_ACC=1 and
// NANELO_API_KEY set, they run against the real API instead, in NANELO_TEST_ZONE or the key's
// only zone. Every record a test creates has a random "tfacc-" name, so a real zone that holds
// other records is safe to use.
type testEnv struct {
	zone      string
	api       *client.Client     // for creating or deleting records behind Terraform's back
	fake      *fakenanelo.Server // nil against the real API
	factories map[string]func() (tfprotov6.ProviderServer, error)
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	if os.Getenv(resource.EnvTfAcc) != "" && os.Getenv("NANELO_API_KEY") != "" {
		env := &testEnv{api: client.New("", os.Getenv("NANELO_API_KEY"), "terraform-provider-nanelo/test")}
		env.zone = os.Getenv("NANELO_TEST_ZONE")
		if env.zone == "" {
			zones, err := env.api.ListZones(context.Background())
			if err != nil || len(zones) != 1 {
				t.Fatalf("set NANELO_TEST_ZONE: cannot pick a zone from %v (err: %v)", zones, err)
			}
			env.zone = zones[0]
		}
		t.Setenv("NANELO_ZONE", "")
		env.factories = factories(&naneloProvider{version: "test"})
		return env
	}

	fake := fakenanelo.New("test-key", "example.org")
	t.Cleanup(fake.Close)
	t.Setenv("NANELO_API_KEY", "test-key")
	t.Setenv("NANELO_ZONE", "")
	return &testEnv{
		zone:      "example.org",
		api:       client.New(fake.BaseURL(), "test-key", "test"),
		fake:      fake,
		factories: factories(&naneloProvider{version: "test", baseURL: fake.BaseURL()}),
	}
}

func factories(p *naneloProvider) map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){"nanelo": providerserver.NewProtocol6WithError(p)}
}

func (e *testEnv) run(t *testing.T, tc resource.TestCase) {
	t.Helper()
	tc.ProtoV6ProviderFactories = e.factories
	if e.fake != nil {
		resource.UnitTest(t, tc)
	} else {
		resource.Test(t, tc)
	}
}

func (e *testEnv) fakeOnly(t *testing.T) {
	if e.fake == nil {
		t.Skip("only runs against the fake API")
	}
}

// name returns a unique record name in the test zone.
func (e *testEnv) name() string {
	return acctest.RandomWithPrefix("tfacc") + "." + e.zone
}

// recordsNamed returns the records named name, as stored by the API.
func (e *testEnv) recordsNamed(name string) ([]client.Record, error) {
	recs, err := e.api.ListRecords(context.Background(), e.zone)
	if err != nil {
		return nil, err
	}
	var out []client.Record
	for _, r := range recs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out, nil
}

// expectStored checks that the API holds exactly the given values for name, as "TYPE VALUE TTL PRIORITY".
func (e *testEnv) expectStored(name string, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		recs, err := e.recordsNamed(name)
		if err != nil {
			return err
		}
		var got []string
		for _, r := range recs {
			var prio int64
			if r.Priority != nil {
				prio = *r.Priority
			}
			got = append(got, fmt.Sprintf("%s %s %d %d", r.Type, r.Value, r.TTL, prio))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			return fmt.Errorf("records named %s:\ngot:  %q\nwant: %q", name, got, want)
		}
		return nil
	}
}
