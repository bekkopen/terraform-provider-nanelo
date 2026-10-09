resource "nanelo_record" "www" {
  name  = "www.example.org"
  type  = "A"
  value = "192.0.2.10"
  ttl   = 300
}

# The apex is addressed by the zone name itself.
resource "nanelo_record" "mx" {
  for_each = { "mx1.example.org." = 10, "mx2.example.org." = 20 }

  name     = "example.org"
  type     = "MX"
  value    = each.key
  priority = each.value
}

# SRV values are "<weight> <port> <target>"; the priority is separate.
resource "nanelo_record" "sip" {
  name     = "_sip._tcp.example.org"
  type     = "SRV"
  value    = "5 5060 sip.example.org."
  priority = 10
}
