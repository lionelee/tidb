// Copyright 2023 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metricsutil

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pingcap/kvproto/pkg/keyspacepb"
	"github.com/pingcap/tidb/config"
	"github.com/pingcap/tidb/domain"
	"github.com/pingcap/tidb/executor"
	"github.com/pingcap/tidb/infoschema"
	"github.com/pingcap/tidb/keyspace"
	"github.com/pingcap/tidb/metrics"
	plannercore "github.com/pingcap/tidb/planner/core"
	"github.com/pingcap/tidb/server"
	"github.com/pingcap/tidb/session"
	txninfo "github.com/pingcap/tidb/session/txninfo"
	"github.com/pingcap/tidb/sessiontxn/isolation"
	statshandler "github.com/pingcap/tidb/statistics/handle"
	kvstore "github.com/pingcap/tidb/store"
	"github.com/pingcap/tidb/store/copr"
	unimetrics "github.com/pingcap/tidb/store/mockstore/unistore/metrics"
	ttlmetrics "github.com/pingcap/tidb/ttl/metrics"
	"github.com/pingcap/tidb/util"
	topsqlreporter "github.com/pingcap/tidb/util/topsql/reporter"
	tikvconfig "github.com/tikv/client-go/v2/config"
	pd "github.com/tikv/pd/client"
)

// RegisterMetrics register metrics with const label 'keyspace_id' if keyspaceName set.
func RegisterMetrics() error {
	cfg := config.GetGlobalConfig()
	if keyspace.IsKeyspaceNameEmpty(cfg.KeyspaceName) || strings.ToLower(cfg.Store) != "tikv" {
		return registerMetrics(nil) // register metrics without label 'keyspace_id'.
	}

	pdAddrs, _, _, err := tikvconfig.ParsePath("tikv://" + cfg.Path)
	if err != nil {
		return err
	}

	timeoutSec := time.Duration(cfg.PDClient.PDServerTimeout) * time.Second
	pdCli, err := pd.NewClient(pdAddrs, pd.SecurityOption{
		CAPath:   cfg.Security.ClusterSSLCA,
		CertPath: cfg.Security.ClusterSSLCert,
		KeyPath:  cfg.Security.ClusterSSLKey,
	}, pd.WithCustomTimeoutOption(timeoutSec))
	if err != nil {
		return err
	}
	defer pdCli.Close()

	keyspaceMeta, err := getKeyspaceMeta(pdCli, cfg.KeyspaceName)
	if err != nil {
		return err
	}

	return registerMetrics(keyspaceMeta)
}

// RegisterMetricsForBR register metrics with const label keyspace_id for BR.
func RegisterMetricsForBR(pdAddrs []string, keyspaceName string) error {
	if keyspace.IsKeyspaceNameEmpty(keyspaceName) {
		return registerMetrics(nil) // register metrics without label 'keyspace_id'.
	}

	timeoutSec := 10 * time.Second
	pdCli, err := pd.NewClient(pdAddrs, pd.SecurityOption{},
		pd.WithCustomTimeoutOption(timeoutSec))
	if err != nil {
		return err
	}
	defer pdCli.Close()

	keyspaceMeta, err := getKeyspaceMeta(pdCli, keyspaceName)
	if err != nil {
		return err
	}

	return registerMetrics(keyspaceMeta)
}

func registerMetrics(keyspaceMeta *keyspacepb.KeyspaceMeta) error {
	if keyspaceMeta != nil {
		metrics.SetKeyspaceLabels(fmt.Sprint(keyspaceMeta.GetId()))
	}

	metrics.InitMetrics()
	metrics.RegisterMetrics()

	copr.InitMetricsVars()
	domain.InitMetricsVars()
	executor.InitMetricsVars()
	infoschema.InitMetricsVars()
	isolation.InitMetricsVars()
	plannercore.InitMetricsVars()
	server.InitMetricsVars()
	session.InitMetricsVars()
	statshandler.InitMetricsVars()
	topsqlreporter.InitMetricsVars()
	ttlmetrics.InitMetricsVars()
	txninfo.InitMetricsVars()

	if config.GetGlobalConfig().Store == "unistore" {
		unimetrics.RegisterMetrics()
	}
	return nil
}

func getKeyspaceMeta(pdCli pd.Client, keyspaceName string) (*keyspacepb.KeyspaceMeta, error) {
	// Load Keyspace meta with retry.
	var keyspaceMeta *keyspacepb.KeyspaceMeta
	err := util.RunWithRetry(util.DefaultMaxRetries, util.RetryInterval, func() (bool, error) {
		var errInner error
		keyspaceMeta, errInner = pdCli.LoadKeyspace(context.TODO(), keyspaceName)
		// Retry when pd not bootstrapped or if keyspace not exists.
		if kvstore.IsNotBootstrappedError(errInner) || kvstore.IsKeyspaceNotExistError(errInner) {
			return true, errInner
		}
		// Do not retry when success or encountered unexpected error.
		return false, errInner
	})
	if err != nil {
		return nil, err
	}

	return keyspaceMeta, nil
}