# Terraform provider for Nanelo

Manages DNS records in [Nanelo](https://nanelo.com) through its [API](https://nanelo.com/docs).

```hcl
provider "nanelo" {} # NANELO_API_KEY; the zone is looked up for domain-bound keys

resource "nanelo_record" "www" {
  name  = "www.example.org"
  type  = "A"
  value = "192.0.2.10"
  ttl   = 300
}
```

See [`docs/`](docs/) for the resource and data source reference.

## How the Nanelo API shapes the provider

The API has four calls (list zones, list records, add record, delete record) and no record IDs.
The details below were verified against the live API and are not in Nanelo's docs:

- **Records are identified by name, type and value.** Delete removes every record that matches,
  and add happily creates exact duplicates. `nanelo_record` therefore refuses to create a record
  that already exists; import it instead.
- **There is no update call.** A `ttl` or `priority` change deletes and re-adds the record, so it
  is briefly absent. It is deliberately an in-place update rather than a replacement: with
  `create_before_destroy`, a replacement would add the new record and then delete both.
- **TTLs are rounded** to 60, 300, 900, 1800, 3600, 7200, 18000, 43200 or 86400 (default 3600).
  Other values are rejected at plan time rather than silently changed.
- **The apex must be sent as `@`.** Sending the zone name creates `example.org.example.org`, and so
  does any name outside the zone. The provider takes fully-qualified names and translates.
- **Inputs are rewritten**: names are lowercased and lose trailing dots, values are trimmed.
  These are rejected at plan time so Terraform's state always matches what Nanelo stores.
- Values are otherwise stored verbatim (`a.example.` and `a.example` differ), compared
  case-insensitively on delete, and cannot contain emoji.

The [client package doc](internal/client/client.go) lists these as well, and
[`internal/fakenanelo`](internal/fakenanelo/fakenanelo.go) reproduces them for tests.

## Development

```sh
make test      # everything, against an in-memory fake of the API
make testacc   # provider tests against the real API: needs NANELO_API_KEY (+ NANELO_TEST_ZONE for team keys)
make generate  # regenerate docs/ from the schema and examples/
```

Acceptance tests only create records named `tfacc-<random>` (plus one TXT record at the apex with a
random value) and delete them afterwards, so they can run against a zone that is in use.
