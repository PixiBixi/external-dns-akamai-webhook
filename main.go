/*
Copyright 2026 Jeremy Delgado.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command external-dns-akamai-webhook serves Akamai Edge DNS to ExternalDNS over
// the webhook provider API.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/PixiBixi/external-dns-akamai-webhook/internal/akamai"
	"github.com/PixiBixi/external-dns-akamai-webhook/internal/config"
	"github.com/PixiBixi/external-dns-akamai-webhook/internal/server"
)

// version is set at build time by the linker.
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Println(version)
		return nil
	}

	level, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("parsing --log-level: %w", err)
	}
	log.SetLevel(level)
	if cfg.LogFormat == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}

	client, err := akamai.NewClient(cfg.Credentials, "ExternalDNS-Akamai-Webhook/"+version)
	if err != nil {
		return err
	}

	p := akamai.New(akamai.Instrument(client), cfg.Provider)

	// SIGTERM is what Kubernetes sends; draining on it keeps an in-flight
	// ApplyChanges from being cut mid-zone during a rollout.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Infof("external-dns-akamai-webhook %s starting", version)
	if cfg.Provider.DryRun {
		log.Warn("dry run: no change will be written to Edge DNS")
	}

	return server.New(server.Options{
		Provider:          p,
		ProviderAddr:      cfg.ProviderAddr,
		HealthAddr:        cfg.HealthAddr,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxBodySize:       cfg.MaxBodySize,
	}).Run(ctx)
}
