package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/bekk/terraform-provider-nanelo/internal/provider"
)

// Run "go generate" to format example terraform files and generate the docs for the registry.
//go:generate terraform fmt -recursive ./examples/
//go:generate go tool tfplugindocs generate --provider-name nanelo

var version = "dev" // set by goreleaser

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/bekk/nanelo",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
