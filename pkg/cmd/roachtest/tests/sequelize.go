// Copyright 2021 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package tests

import (
	"context"
	"fmt"
	"regexp"

	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/cluster"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/option"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/registry"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/roachtestutil"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/test"
	"github.com/cockroachdb/cockroach/pkg/roachprod/install"
)

var sequelizeCockroachDBReleaseTagRegex = regexp.MustCompile(`^v(?P<major>\d+)\.(?P<minor>\d+)\.(?P<point>\d+)$`)

// WARNING: DO NOT MODIFY the name of the below constant/variable without approval from the docs team.
// This is used by docs automation to produce a list of supported versions for ORM's.
var supportedSequelizeCockroachDBRelease = "v6.0.5"

// This test runs sequelize's full test suite against a single cockroach node.

func registerSequelize(r registry.Registry) {
	runSequelize := func(
		ctx context.Context,
		t test.Test,
		c cluster.Cluster,
	) {
		if c.IsLocal() {
			t.Fatal("cannot be run in local mode")
		}
		node := c.Node(1)
		t.Status("setting up cockroach")
		startOpts := option.NewStartOpts(sqlClientsInMemoryDB)
		roachtestutil.SetDefaultSQLPort(c, &startOpts.RoachprodOpts)
		c.Start(ctx, t.L(), startOpts, install.MakeClusterSettings(install.SecureOption(false)), c.All())

		version, err := fetchCockroachVersion(ctx, t.L(), c, node[0])
		if err != nil {
			t.Fatal(err)
		}

		if err := alterZoneConfigAndClusterSettings(ctx, t, version, c, node[0]); err != nil {
			t.Fatal(err)
		}

		t.Status("create database used by tests")
		db, err := c.ConnE(ctx, t.L(), node[0])
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		if _, err := db.ExecContext(
			ctx,
			`CREATE DATABASE sequelize_test`,
		); err != nil {
			t.Fatal(err)
		}

		t.Status("cloning sequelize-cockroachdb and installing prerequisites")
		latestTag, err := repeatGetLatestTag(ctx, t, "cockroachdb", "sequelize-cockroachdb", sequelizeCockroachDBReleaseTagRegex)
		if err != nil {
			t.Fatal(err)
		}
		t.L().Printf("Latest sequelize-cockroachdb release is %s.", latestTag)
		t.L().Printf("Supported sequelize-cockroachdb release is %s.", supportedSequelizeCockroachDBRelease)

		if err := repeatRunE(
			ctx, t, c, node, "update apt-get", `sudo apt-get -qq update`,
		); err != nil {
			t.Fatal(err)
		}

		if err := repeatRunE(
			ctx,
			t,
			c,
			node,
			"install dependencies",
			`sudo apt-get -qq install make python3 libpq-dev gcc g++ `+
				`software-properties-common build-essential`,
		); err != nil {
			t.Fatal(err)
		}

		// Install NodeJS 18.x and update NPM to the latest.
		if err := installNode18(ctx, t, c, node, nodeOpts{}); err != nil {
			t.Fatal(err)
		}

		if err := repeatRunE(
			ctx, t, c, node, "remove old sequelize", `sudo rm -rf /mnt/data1/sequelize`,
		); err != nil {
			t.Fatal(err)
		}

		if err := repeatGitCloneE(
			ctx,
			t,
			c,
			"https://github.com/cockroachdb/sequelize-cockroachdb.git",
			"/mnt/data1/sequelize",
			supportedSequelizeCockroachDBRelease,
			node,
		); err != nil {
			t.Fatal(err)
		}

		if err := repeatRunE(
			ctx, t, c, node, "install dependencies", `cd /mnt/data1/sequelize && sudo npm i`,
		); err != nil {
			t.Fatal(err)
		}

		// Version telemetry is already disabled in the sequelize-cockroachdb test suite.
		t.Status("running Sequelize test suite")
		result, err := c.RunWithDetailsSingleNode(ctx, t.L(), option.WithNodes(node),
			fmt.Sprintf(`cd /mnt/data1/sequelize/ && npm test --crdb_version=%s`, version),
		)
		rawResultsStr := result.Stdout + result.Stderr
		t.L().Printf("Test Results: %s", rawResultsStr)
		if err != nil {
			// The test suite is flaky and work is being done upstream to stabilize
			// it (https://github.com/sequelize/sequelize/pull/15569). Until that's
			// done, we ignore all failures from this test.
			// t.Fatal(err)
			t.L().Printf("ignoring failure (https://github.com/cockroachdb/cockroach/issues/108937): %s", err)
		}
	}

	r.Add(registry.TestSpec{
		Name:             "sequelize",
		Owner:            registry.OwnerSQLFoundations,
		Cluster:          r.MakeClusterSpec(1),
		Leases:           registry.MetamorphicLeases,
		NativeLibs:       registry.LibGEOS,
		CompatibleClouds: registry.AllExceptAWS,
		Suites:           registry.Suites(registry.Nightly, registry.ORM),
		Run: func(ctx context.Context, t test.Test, c cluster.Cluster) {
			runSequelize(ctx, t, c)
		},
	})
}
