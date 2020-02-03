/*
Copyright © 2020 Red Hat, Inc.

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

package main_test

import (
	"github.com/RedHatInsights/insights-results-aggregator"
	"os"
	"testing"
)

func TestLoadConfiguration(t *testing.T) {
	err := os.Unsetenv("INSIGHTS_RESULTS_AGGREGATOR_CONFIG_FILE")
	if err != nil {
		t.Fatal(err)
	}
	main.LoadConfiguration("tests/config1")
}

func TestLoadConfigurationEnvVariable(t *testing.T) {
	err := os.Setenv("INSIGHTS_RESULTS_AGGREGATOR_CONFIG_FILE", "tests/config1")
	if err != nil {
		t.Fatal(err)
	}
	main.LoadConfiguration("foobar")
}

func TestLoadingConfigurationFailure(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("The code did not panic as expected")
		}
	}()
	err := os.Unsetenv("INSIGHTS_RESULTS_AGGREGATOR_CONFIG_FILE")
	if err != nil {
		t.Fatal(err)
	}
	main.LoadConfiguration("this does not exist")
}

func TestLoadBrokerConfiguration(t *testing.T) {
	TestLoadConfiguration(t)
	brokerCfg := main.LoadBrokerConfiguration()
	if brokerCfg.Address != "localhost:9092" {
		t.Fatal("Improper broker address", brokerCfg.Address)
	}
	if brokerCfg.Topic != "platform.results.ccx" {
		t.Fatal("Improper broker topic", brokerCfg.Topic)
	}
	if brokerCfg.Group != "aggregator" {
		t.Fatal("Improper broker group", brokerCfg.Group)
	}
}
