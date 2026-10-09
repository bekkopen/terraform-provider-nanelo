package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// nanelo_zones

func newZonesDataSource() datasource.DataSource { return &zonesDataSource{} }

type zonesDataSource struct{ data *providerData }

func (d *zonesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zones"
}

func (d *zonesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the zones the API key has access to.",
		Attributes: map[string]schema.Attribute{
			"zones": schema.ListAttribute{
				MarkdownDescription: "Zone names.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

func (d *zonesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if req.ProviderData != nil {
		d.data = req.ProviderData.(*providerData)
	}
}

func (d *zonesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	zones, err := d.data.client.ListZones(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list zones", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zones"), zones)...)
}

// nanelo_records

func newRecordsDataSource() datasource.DataSource { return &recordsDataSource{} }

type recordsDataSource struct{ data *providerData }

type recordsDataSourceModel struct {
	Zone    types.String          `tfsdk:"zone"`
	Name    types.String          `tfsdk:"name"`
	Type    types.String          `tfsdk:"type"`
	Records []recordsDataSourceRR `tfsdk:"records"`
}

type recordsDataSourceRR struct {
	Name         string `tfsdk:"name"`
	Type         string `tfsdk:"type"`
	Value        string `tfsdk:"value"`
	TTL          int64  `tfsdk:"ttl"`
	Priority     int64  `tfsdk:"priority"`
	SystemRecord bool   `tfsdk:"system_record"`
}

func (d *recordsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_records"
}

func (d *recordsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the records in a zone, including the NS and SOA records Nanelo manages itself.",
		Attributes: map[string]schema.Attribute{
			"zone": schema.StringAttribute{
				MarkdownDescription: "Zone to list. Defaults to the provider's `zone`, or to the API key's only zone.",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String{zoneName()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Only return records with this fully-qualified name.",
				Optional:            true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Only return records of this type.",
				Optional:            true,
			},
			"records": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":          schema.StringAttribute{Computed: true, MarkdownDescription: "Fully-qualified name."},
						"type":          schema.StringAttribute{Computed: true},
						"value":         schema.StringAttribute{Computed: true},
						"ttl":           schema.Int64Attribute{Computed: true},
						"priority":      schema.Int64Attribute{Computed: true},
						"system_record": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether Nanelo manages the record itself."},
					},
				},
			},
		},
	}
}

func (d *recordsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if req.ProviderData != nil {
		d.data = req.ProviderData.(*providerData)
	}
}

func (d *recordsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg recordsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone, err := d.data.resolveZone(ctx, cfg.Zone.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("zone"), "Cannot determine zone", err.Error())
		return
	}
	recs, err := d.data.client.ListRecords(ctx, zone)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list records", fmt.Sprintf("Listing records in zone %s: %s", zone, err))
		return
	}

	cfg.Zone = types.StringValue(zone)
	cfg.Records = []recordsDataSourceRR{}
	for _, r := range recs {
		if (!cfg.Name.IsNull() && !strings.EqualFold(r.Name, strings.TrimSuffix(cfg.Name.ValueString(), "."))) ||
			(!cfg.Type.IsNull() && !strings.EqualFold(r.Type, cfg.Type.ValueString())) {
			continue
		}
		rr := recordsDataSourceRR{Name: r.Name, Type: r.Type, Value: r.Value, TTL: r.TTL, SystemRecord: r.SystemRecord}
		if r.Priority != nil {
			rr.Priority = *r.Priority
		}
		cfg.Records = append(cfg.Records, rr)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
