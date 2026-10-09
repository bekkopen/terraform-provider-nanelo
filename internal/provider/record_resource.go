package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bekk/terraform-provider-nanelo/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*recordResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*recordResource)(nil)
	_ resource.ResourceWithImportState = (*recordResource)(nil)
)

func newRecordResource() resource.Resource { return &recordResource{} }

type recordResource struct {
	data *providerData
}

type recordModel struct {
	ID       types.String `tfsdk:"id"`
	Zone     types.String `tfsdk:"zone"`
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Value    types.String `tfsdk:"value"`
	TTL      types.Int64  `tfsdk:"ttl"`
	Priority types.Int64  `tfsdk:"priority"`
}

func (m recordModel) record() client.Record {
	prio := m.Priority.ValueInt64()
	return client.Record{
		Name:     m.Name.ValueString(),
		Type:     m.Type.ValueString(),
		Value:    m.Value.ValueString(),
		TTL:      m.TTL.ValueInt64(),
		Priority: &prio,
	}
}

func (m recordModel) describe() string {
	return fmt.Sprintf("%s record %s with value %q", m.Type.ValueString(), m.Name.ValueString(), m.Value.ValueString())
}

// recordID builds the import ID. The value goes last so it may contain "/".
func recordID(zone, name, typ, value string) string {
	return strings.Join([]string{zone, name, typ, value}, "/")
}

func (r *recordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_record"
}

func (r *recordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A single DNS record value.\n\n" +
			"Nanelo records have no IDs: a record is identified by its zone, name, type and value, and deleting " +
			"it deletes every record with the same name, type and value. For that reason this resource refuses to " +
			"create a record that already exists (import it instead), and two resources must never describe the same record.\n\n" +
			"Nanelo has no update operation, so changing `ttl` or `priority` deletes and re-adds the record, " +
			"leaving a short window where it does not exist. Changing anything else replaces the resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<zone>/<name>/<type>/<value>`. Also the import ID.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone": schema.StringAttribute{
				MarkdownDescription: "Zone the record belongs to, e.g. `example.org`. Defaults to the provider's `zone`, " +
					"or to the API key's only zone.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{zoneName()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Fully-qualified record name, e.g. `www.example.org`. Use the zone name itself for the apex, " +
					"and `*.example.org` for a wildcard.",
				Required:      true,
				Validators:    []validator.String{recordName()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Record type. One of " + backtickList(client.SupportedTypes) + ".",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(client.SupportedTypes...)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value": schema.StringAttribute{
				MarkdownDescription: "Record value, stored verbatim. Hostnames are not normalized, so `target.example.org` and " +
					"`target.example.org.` are different values. For `MX`, put the preference in `priority`. For `SRV`, " +
					"use `<weight> <port> <target>` and put the priority in `priority`. TXT values are not quoted or split.",
				Required:      true,
				Validators:    []validator.String{recordValue()},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"ttl": schema.Int64Attribute{
				MarkdownDescription: fmt.Sprintf("TTL in seconds. Nanelo only supports %s. Defaults to `%d`.",
					backtickList(client.SupportedTTLs), client.DefaultTTL),
				Optional:   true,
				Computed:   true,
				Default:    int64default.StaticInt64(client.DefaultTTL),
				Validators: []validator.Int64{int64validator.OneOf(client.SupportedTTLs...)},
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: "Priority for `MX` and `SRV` records. Defaults to `0`.",
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(0),
				Validators:          []validator.Int64{int64validator.Between(0, 65535)},
			},
		},
	}
}

func (r *recordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData != nil {
		r.data = req.ProviderData.(*providerData)
	}
}

// ModifyPlan resolves the zone at plan time so it, and the ID, are known in the plan, and
// checks that the name lies inside the zone. Names outside it would be silently rewritten
// by Nanelo to "<name>.<zone>".
func (r *recordResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.data == nil {
		return
	}
	var plan recordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var configZone types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("zone"), &configZone)...)
	if configZone.IsNull() && plan.Zone.IsUnknown() {
		zone, err := r.data.resolveZone(ctx, "")
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("zone"), "Cannot determine zone", err.Error())
			return
		}
		plan.Zone = types.StringValue(zone)
	}

	if !plan.Zone.IsUnknown() && !plan.Name.IsUnknown() {
		zone, name := plan.Zone.ValueString(), plan.Name.ValueString()
		if name != zone && !strings.HasSuffix(name, "."+zone) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Name outside zone",
				fmt.Sprintf("name must be a fully-qualified name in zone %q, such as %q, or %q for the apex. Got %q.",
					zone, "www."+zone, zone, name))
			return
		}
	}

	if !plan.Zone.IsUnknown() && !plan.Name.IsUnknown() && !plan.Type.IsUnknown() && !plan.Value.IsUnknown() {
		plan.ID = types.StringValue(recordID(plan.Zone.ValueString(), plan.Name.ValueString(), plan.Type.ValueString(), plan.Value.ValueString()))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, plan)...)
}

func (r *recordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan recordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone := plan.Zone.ValueString()
	defer r.data.lockZone(zone)()

	// The API happily creates exact duplicates, and a later delete would remove both, so an
	// existing record must be imported rather than created again.
	existing, diags := r.find(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(existing) > 0 {
		resp.Diagnostics.AddError("Record already exists",
			fmt.Sprintf("A %s already exists in zone %s. Deleting a Nanelo record deletes every record with the same name, "+
				"type and value, so Terraform will not create a second one. Import it instead, using the ID %q.",
				plan.describe(), zone, plan.ID.ValueString()))
		return
	}

	if err := r.data.client.AddRecord(ctx, zone, plan.record()); err != nil {
		resp.Diagnostics.AddError("Failed to create record", fmt.Sprintf("Creating %s: %s", plan.describe(), err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *recordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state recordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	matches, diags := r.find(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(matches) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}
	if matches[0].SystemRecord {
		resp.Diagnostics.AddError("Cannot manage system record",
			fmt.Sprintf("The %s is managed by Nanelo and cannot be managed by Terraform.", state.describe()))
		return
	}
	if len(matches) > 1 {
		resp.Diagnostics.AddWarning("Duplicate records",
			fmt.Sprintf("Zone %s contains %d copies of the %s. Nanelo cannot tell them apart, so updating or destroying "+
				"this resource affects all of them.", state.Zone.ValueString(), len(matches), state.describe()))
	}

	// Name, type and value identify the record and are left as configured; Nanelo compares
	// values case-insensitively, so copying its spelling back could cause a spurious replace.
	m := matches[0]
	state.TTL = types.Int64Value(m.TTL)
	state.Priority = types.Int64Value(0)
	if m.Priority != nil {
		state.Priority = types.Int64Value(*m.Priority)
	}
	state.ID = types.StringValue(recordID(state.Zone.ValueString(), state.Name.ValueString(), state.Type.ValueString(), state.Value.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update only ever changes ttl or priority; every other attribute forces replacement. The API
// has no update call, and adding the new record before deleting the old one is not possible
// because the delete would match both, so the record is briefly absent.
func (r *recordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan recordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone := plan.Zone.ValueString()
	defer r.data.lockZone(zone)()

	err := r.data.client.DeleteRecord(ctx, zone, plan.Name.ValueString(), plan.Type.ValueString(), plan.Value.ValueString())
	if err != nil && !errors.Is(err, client.ErrNoMatchingRecords) {
		resp.Diagnostics.AddError("Failed to update record", fmt.Sprintf("Deleting %s before re-adding it: %s", plan.describe(), err))
		return
	}
	if err := r.data.client.AddRecord(ctx, zone, plan.record()); err != nil {
		resp.Diagnostics.AddError("Failed to update record",
			fmt.Sprintf("The %s was deleted, but re-adding it failed: %s. Run terraform apply again to recreate it.", plan.describe(), err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *recordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state recordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	zone := state.Zone.ValueString()
	defer r.data.lockZone(zone)()

	err := r.data.client.DeleteRecord(ctx, zone, state.Name.ValueString(), state.Type.ValueString(), state.Value.ValueString())
	if err != nil && !errors.Is(err, client.ErrNoMatchingRecords) {
		resp.Diagnostics.AddError("Failed to delete record", fmt.Sprintf("Deleting %s: %s", state.describe(), err))
	}
}

func (r *recordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <zone>/<name>/<type>/<value>, e.g. example.org/www.example.org/A/192.0.2.1. Got %q.", req.ID))
		return
	}
	zone, name := strings.ToLower(parts[0]), strings.ToLower(strings.TrimSuffix(parts[1], "."))
	typ, value := strings.ToUpper(parts[2]), parts[3]
	for attr, v := range map[string]string{"id": recordID(zone, name, typ, value), "zone": zone, "name": name, "type": typ, "value": value} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(attr), v)...)
	}
}

// find returns the records in m's zone that Nanelo considers the same record as m, i.e. the
// ones a delete of m would remove.
func (r *recordResource) find(ctx context.Context, m recordModel) ([]client.Record, diag.Diagnostics) {
	var diags diag.Diagnostics
	recs, err := r.data.client.ListRecords(ctx, m.Zone.ValueString())
	if err != nil {
		diags.AddError("Failed to list records", fmt.Sprintf("Listing records in zone %s: %s", m.Zone.ValueString(), err))
		return nil, diags
	}
	var out []client.Record
	for _, rec := range recs {
		if strings.EqualFold(rec.Name, m.Name.ValueString()) && strings.EqualFold(rec.Type, m.Type.ValueString()) &&
			strings.EqualFold(rec.Value, m.Value.ValueString()) {
			out = append(out, rec)
		}
	}
	return out, diags
}

func backtickList[T any](items []T) string {
	s := make([]string, len(items))
	for i, it := range items {
		s[i] = fmt.Sprintf("`%v`", it)
	}
	return strings.Join(s, ", ")
}
