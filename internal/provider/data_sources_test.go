package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccDataSources(t *testing.T) {
	env := newTestEnv(t)
	name := env.name()

	env.run(t, resource.TestCase{
		Steps: []resource.TestStep{
			{
				Config: recordConfig(name, "TXT", "hello", "") + `
data "nanelo_zones" "all" {}

data "nanelo_records" "test" {
  name       = nanelo_record.test.name
  type       = "TXT"
  depends_on = [nanelo_record.test]
}

data "nanelo_records" "ns" {
  name = nanelo_record.test.zone
  type = "NS"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckTypeSetElemAttr("data.nanelo_zones.all", "zones.*", env.zone),
					resource.TestCheckResourceAttr("data.nanelo_records.test", "zone", env.zone),
					resource.TestCheckResourceAttr("data.nanelo_records.test", "records.#", "1"),
					resource.TestCheckResourceAttr("data.nanelo_records.test", "records.0.name", name),
					resource.TestCheckResourceAttr("data.nanelo_records.test", "records.0.value", "hello"),
					resource.TestCheckResourceAttr("data.nanelo_records.test", "records.0.ttl", "3600"),
					resource.TestCheckResourceAttr("data.nanelo_records.test", "records.0.system_record", "false"),
					resource.TestCheckResourceAttr("data.nanelo_records.ns", "records.0.system_record", "true"),
				),
			},
		},
	})
}
