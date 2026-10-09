package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/bekkopen/terraform-provider-nanelo/internal/client"
	"github.com/bekkopen/terraform-provider-nanelo/internal/fakenanelo"
)

func recordConfig(name, typ, value, extra string) string {
	return fmt.Sprintf(`
resource "nanelo_record" "test" {
  name  = %q
  type  = %q
  value = %q
  %s
}
`, name, typ, value, extra)
}

func TestAccRecord_lifecycle(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()

	env.run(t, resource.TestCase{
		CheckDestroy: env.expectStored(name),
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "A", "192.0.2.1", "ttl = 300"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("nanelo_record.test", "id", env.zone+"/"+name+"/A/192.0.2.1"),
					resource.TestCheckResourceAttr("nanelo_record.test", "zone", env.zone),
					resource.TestCheckResourceAttr("nanelo_record.test", "ttl", "300"),
					resource.TestCheckResourceAttr("nanelo_record.test", "priority", "0"),
					env.expectStored(name, "A 192.0.2.1 300 0"),
				),
			},
			{
				ResourceName:      "nanelo_record.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// TTL changes are applied in place, and must not leave a duplicate behind.
				Config: recordConfig(name, "A", "192.0.2.1", "ttl = 3600"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionUpdate),
				}},
				Check: env.expectStored(name, "A 192.0.2.1 3600 0"),
			},
			{
				Config: recordConfig(name, "A", "192.0.2.2", "ttl = 3600"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: env.expectStored(name, "A 192.0.2.2 3600 0"),
			},
		},
	})
}

func TestAccRecord_createBeforeDestroy(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()
	lifecycle := "lifecycle {\n    create_before_destroy = true\n  }"

	env.run(t, resource.TestCase{
		CheckDestroy: env.expectStored(name),
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "A", "192.0.2.1", lifecycle),
				Check:  env.expectStored(name, "A 192.0.2.1 3600 0"),
			},
			{
				// Would delete both copies if a TTL change were a replacement.
				Config: recordConfig(name, "A", "192.0.2.1", "ttl = 300\n  "+lifecycle),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionUpdate),
				}},
				Check: env.expectStored(name, "A 192.0.2.1 300 0"),
			},
			{
				Config: recordConfig(name, "A", "192.0.2.2", "ttl = 300\n  "+lifecycle),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionCreateBeforeDestroy),
				}},
				Check: env.expectStored(name, "A 192.0.2.2 300 0"),
			},
		},
	})
}

func TestAccRecord_mxPriority(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()

	env.run(t, resource.TestCase{
		CheckDestroy: env.expectStored(name),
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "MX", "mail.example.com.", "priority = 10"),
				Check:  env.expectStored(name, "MX mail.example.com. 3600 10"),
			},
			{
				Config: recordConfig(name, "MX", "mail.example.com.", "priority = 20"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionUpdate),
				}},
				Check: env.expectStored(name, "MX mail.example.com. 3600 20"),
			},
			{
				ResourceName:      "nanelo_record.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccRecord_apex(t *testing.T) {
	env := newTestEnv(t)
	value := "tfacc-apex-" + acctest.RandString(8)

	env.run(t, resource.TestCase{
		CheckDestroy: func(*terraform.State) error {
			recs, err := env.recordsNamed(env.zone)
			for _, r := range recs {
				if r.Value == value {
					return fmt.Errorf("apex record still exists: %+v", r)
				}
			}
			return err
		},
		Steps: []resource.TestStep{
			{
				Config: recordConfig(env.zone, "TXT", value, ""),
				Check: func(*terraform.State) error {
					recs, err := env.recordsNamed(env.zone)
					for _, r := range recs {
						if r.Type == "TXT" && r.Value == value {
							return nil
						}
					}
					return fmt.Errorf("apex TXT record not found (err: %v); was it created as %s.%s?", err, env.zone, env.zone)
				},
			},
		},
	})
}

// TXT values may contain "/", which the import ID must survive.
func TestAccRecord_txtImport(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()
	value := "v=DKIM1; k=rsa; p=MIIBIjANBg/kqhkiG9w0+BAQEFAAOC=="

	env.run(t, resource.TestCase{
		CheckDestroy: env.expectStored(name),
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "TXT", value, ""),
				Check:  env.expectStored(name, "TXT "+value+" 3600 0"),
			},
			{
				ResourceName:      "nanelo_record.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccRecord_refusesExistingRecord(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()
	if err := env.api.AddRecord(context.Background(), env.zone, client.Record{Name: name, Type: "A", Value: "192.0.2.1", TTL: 3600}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.api.DeleteRecord(context.Background(), env.zone, name, "A", "192.0.2.1") })

	env.run(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      recordConfig(name, "A", "192.0.2.1", ""),
				ExpectError: regexp.MustCompile(`Record already exists`),
			},
			{
				// The pre-existing record must be untouched, and not duplicated.
				Config: `# empty`,
				Check:  env.expectStored(name, "A 192.0.2.1 3600 0"),
			},
		},
	})
}

func TestAccRecord_recreatedAfterOutOfBandDelete(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()

	env.run(t, resource.TestCase{
		CheckDestroy: env.expectStored(name),
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "A", "192.0.2.1", ""),
			},
			{
				PreConfig: func() {
					if err := env.api.DeleteRecord(context.Background(), env.zone, name, "A", "192.0.2.1"); err != nil {
						t.Fatal(err)
					}
				},
				Config: recordConfig(name, "A", "192.0.2.1", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionCreate),
				}},
				Check: env.expectStored(name, "A 192.0.2.1 3600 0"),
			},
		},
	})
}

func TestAccRecord_ttlDriftDetected(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()

	env.run(t, resource.TestCase{
		CheckDestroy: env.expectStored(name),
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "A", "192.0.2.1", ""),
			},
			{
				PreConfig: func() {
					ctx := context.Background()
					if err := env.api.DeleteRecord(ctx, env.zone, name, "A", "192.0.2.1"); err != nil {
						t.Fatal(err)
					}
					if err := env.api.AddRecord(ctx, env.zone, client.Record{Name: name, Type: "A", Value: "192.0.2.1", TTL: 60}); err != nil {
						t.Fatal(err)
					}
				},
				Config: recordConfig(name, "A", "192.0.2.1", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("nanelo_record.test", plancheck.ResourceActionUpdate),
				}},
				Check: env.expectStored(name, "A 192.0.2.1 3600 0"),
			},
		},
	})
}

func TestAccRecord_validation(t *testing.T) {
	env := newTestEnv(t)
	env.fakeOnly(t)
	name := env.name()

	for _, tc := range []struct{ config, err string }{
		{recordConfig("WWW."+env.zone, "A", "192.0.2.1", ""), `stores names in lowercase`},
		{recordConfig(name+".", "A", "192.0.2.1", ""), `strips trailing dots`},
		{recordConfig("www", "A", "192.0.2.1", ""), `Name outside zone`},
		{recordConfig("www.example.com", "A", "192.0.2.1", ""), `Name outside zone`},
		{recordConfig(name, "DS", "1 2 3 abc", ""), `value must be one of`},
		{recordConfig(name, "TXT", " padded", ""), `trims surrounding whitespace`},
		{recordConfig(name, "TXT", "hi 🙂", ""), `such as emoji`},
		{recordConfig(name, "A", "192.0.2.1", "ttl = 14400"), `value must be one of`},
	} {
		env.run(t, resource.TestCase{
			Steps: []resource.TestStep{{Config: tc.config, PlanOnly: true, ExpectError: regexp.MustCompile(tc.err)}},
		})
	}
}

func TestAccRecord_teamKeyNeedsZone(t *testing.T) {
	env := newTestEnv(t)
	env.fakeOnly(t)
	fake := fakenanelo.New("test-key", "a.example", "b.example")
	t.Cleanup(fake.Close)
	env.factories = factories(&naneloProvider{version: "test", baseURL: fake.BaseURL()})

	env.run(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config:      recordConfig("www.b.example", "A", "192.0.2.1", ""),
				ExpectError: regexp.MustCompile(`access to 2 zones`),
			},
			{
				Config: `provider "nanelo" {
  zone = "b.example"
}
` + recordConfig("www.b.example", "A", "192.0.2.1", ""),
				Check: func(*terraform.State) error {
					if recs := fake.Records("b.example"); len(recs) != 1 || recs[0].Name != "www.b.example" {
						return fmt.Errorf("unexpected records in b.example: %+v", recs)
					}
					return nil
				},
			},
		},
	})
}
