// Copyright 2020 Red Hat, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/RedHatInsights/insights-results-aggregator/types"
)

// RuleToggle is a type for user's vote
type RuleToggle int

const (
	// RuleToggleDisable indicates the rule has been disabled
	RuleToggleDisable RuleToggle = 1
	// RuleToggleEnable indicates the rule has been (re)enabled
	RuleToggleEnable RuleToggle = 0
)

// ClusterRuleToggle represents a record from rule_cluster_toggle
type ClusterRuleToggle struct {
	ClusterID  types.ClusterName
	RuleID     types.RuleID
	Disabled   RuleToggle
	DisabledAt sql.NullTime
	EnabledAt  sql.NullTime
	UpdatedAt  sql.NullTime
}

// ToggleRuleForCluster toggles rule for specified cluster
func (storage DBStorage) ToggleRuleForCluster(
	clusterID types.ClusterName, ruleID types.RuleID, errorKey types.ErrorKey, ruleToggle RuleToggle,
) error {

	var query string
	var enabledAt, disabledAt, updatedAt sql.NullTime

	now := time.Now()
	updatedAt = sql.NullTime{Time: now, Valid: true}

	switch ruleToggle {
	case RuleToggleDisable:
		disabledAt = updatedAt
	case RuleToggleEnable:
		enabledAt = updatedAt
	default:
		return fmt.Errorf("Unexpected rule toggle value")
	}

	query = `
		INSERT INTO cluster_rule_toggle(
			cluster_id, rule_id, error_key, disabled, disabled_at, enabled_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (cluster_id, rule_id, error_key) DO UPDATE SET
			disabled = $4,
			disabled_at = $5,
			enabled_at = $6,
			updated_at = $7
	`

	_, err := storage.connection.Exec(
		query,
		clusterID,
		ruleID,
		errorKey,
		ruleToggle,
		disabledAt,
		enabledAt,
		now,
	)
	if err != nil {
		log.Error().Err(err).Msg("Error during execution SQL exec for cluster rule toggle")
		return err
	}

	return nil
}

// GetFromClusterRuleToggle gets a rule from cluster_rule_toggle
func (storage DBStorage) GetFromClusterRuleToggle(
	clusterID types.ClusterName, ruleID types.RuleID,
) (*ClusterRuleToggle, error) {
	var disabledRule ClusterRuleToggle

	// query has LIMIT 1 and ORDER BY updated_at because of old functionality where
	// disabling was per USER (compared to per CLUSTER now) therefore it'd be possible
	// to retrieve more than 1 record from this query
	query := `
	SELECT
		cluster_id,
		rule_id,
		disabled,
		disabled_at,
		enabled_at,
		updated_at
	FROM
		cluster_rule_toggle
	WHERE
		cluster_id = $1 AND
		rule_id = $2
	ORDER BY
		updated_at DESC
	LIMIT 1
	`

	err := storage.connection.QueryRow(
		query,
		clusterID,
		ruleID,
	).Scan(
		&disabledRule.ClusterID,
		&disabledRule.RuleID,
		&disabledRule.Disabled,
		&disabledRule.DisabledAt,
		&disabledRule.EnabledAt,
		&disabledRule.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, &types.ItemNotFoundError{ItemID: ruleID}
	}

	return &disabledRule, err
}

// GetTogglesForRules gets enable/disable toggle for rules
func (storage DBStorage) GetTogglesForRules(
	clusterID types.ClusterName, rulesReport []types.RuleOnReport,
) (map[types.RuleID]bool, error) {
	ruleIDs := make([]string, 0)
	for _, rule := range rulesReport {
		ruleIDs = append(ruleIDs, string(rule.Module))
	}

	toggles := make(map[types.RuleID]bool)

	query := `
	SELECT
		rule_id,
		disabled
	FROM
		cluster_rule_toggle
	WHERE
		cluster_id = $1 AND
		rule_id in (%v)
	`
	whereInStatement := "'" + strings.Join(ruleIDs, "','") + "'"
	query = fmt.Sprintf(query, whereInStatement)

	rows, err := storage.connection.Query(query, clusterID)
	if err != nil {
		return toggles, err
	}
	defer closeRows(rows)

	for rows.Next() {
		var (
			ruleID   types.RuleID
			disabled bool
		)

		err = rows.Scan(&ruleID, &disabled)

		if err != nil {
			log.Error().Err(err).Msg("GetFromClusterRulesToggle")
			return nil, err
		}

		toggles[ruleID] = disabled
	}

	return toggles, nil
}

// DeleteFromRuleClusterToggle deletes a record from the table rule_cluster_toggle. Only exposed in debug mode.
func (storage DBStorage) DeleteFromRuleClusterToggle(
	clusterID types.ClusterName, ruleID types.RuleID,
) error {
	query := `
	DELETE FROM
		cluster_rule_toggle
	WHERE
		cluster_id = $1 AND
		rule_id = $2
	`
	_, err := storage.connection.Exec(query, clusterID, ruleID)
	return err
}
