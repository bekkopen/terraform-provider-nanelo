package provider

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bekk/terraform-provider-nanelo/internal/client"
)

var _ provider.Provider = (*naneloProvider)(nil)

type naneloProvider struct {
	version string
	baseURL string // overridden in tests
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &naneloProvider{version: version} }
}

type providerModel struct {
	APIKey types.String `tfsdk:"api_key"`
	Zone   types.String `tfsdk:"zone"`
}

func (p *naneloProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "nanelo"
	resp.Version = p.version
}

func (p *naneloProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages DNS records in [Nanelo](https://nanelo.com).",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				MarkdownDescription: "Nanelo API key. Can also be set with the `NANELO_API_KEY` environment variable. " +
					"Keys are either bound to a single domain or to a team/workspace.",
				Optional:  true,
				Sensitive: true,
			},
			"zone": schema.StringAttribute{
				MarkdownDescription: "Default zone for resources and data sources that don't set `zone`. Can also be set with " +
					"the `NANELO_ZONE` environment variable. Not needed for domain-bound keys, where the zone is looked up.",
				Optional:   true,
				Validators: []validator.String{zoneName()},
			},
		},
	}
}

func (p *naneloProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.APIKey.IsUnknown() || cfg.Zone.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration",
			"api_key and zone must be known before Terraform applies, so they cannot depend on values from other resources.")
		return
	}

	apiKey := cmp.Or(cfg.APIKey.ValueString(), os.Getenv("NANELO_API_KEY"))
	if apiKey == "" {
		resp.Diagnostics.AddError("Missing Nanelo API key", "Set api_key in the provider block or the NANELO_API_KEY environment variable.")
		return
	}

	data := &providerData{
		client:      client.New(p.baseURL, apiKey, "terraform-provider-nanelo/"+p.version),
		defaultZone: cmp.Or(cfg.Zone.ValueString(), os.Getenv("NANELO_ZONE")),
	}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *naneloProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{newRecordResource}
}

func (p *naneloProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{newZonesDataSource, newRecordsDataSource}
}

// providerData is shared by all resources and data sources.
type providerData struct {
	client      *client.Client
	defaultZone string

	discoverOnce sync.Once
	discovered   string
	discoverErr  error

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// resolveZone returns explicit if set, then the provider's default zone, and finally the only
// zone the API key has access to.
func (d *providerData) resolveZone(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if d.defaultZone != "" {
		return d.defaultZone, nil
	}
	d.discoverOnce.Do(func() {
		zones, err := d.client.ListZones(ctx)
		switch {
		case err != nil:
			d.discoverErr = fmt.Errorf("looking up zones for the API key: %w", err)
		case len(zones) == 1:
			d.discovered = zones[0]
		default:
			d.discoverErr = fmt.Errorf("the API key has access to %d zones (%s), so zone must be set on the resource or the provider",
				len(zones), strings.Join(zones, ", "))
		}
	})
	return d.discovered, d.discoverErr
}

// lockZone serializes writes to a zone. Without it, two resources creating the same record in
// parallel could both pass the existence check and create a duplicate.
func (d *providerData) lockZone(zone string) func() {
	d.locksMu.Lock()
	if d.locks == nil {
		d.locks = map[string]*sync.Mutex{}
	}
	mu, ok := d.locks[zone]
	if !ok {
		mu = &sync.Mutex{}
		d.locks[zone] = mu
	}
	d.locksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}
