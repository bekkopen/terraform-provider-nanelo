terraform {
  required_providers {
    nanelo = {
      source = "bekk/nanelo"
    }
  }
}

# The API key can also be set with NANELO_API_KEY. With a domain-bound key the zone is
# looked up automatically; team keys need zone here or on each resource.
provider "nanelo" {
  api_key = var.nanelo_api_key
  zone    = "example.org"
}
