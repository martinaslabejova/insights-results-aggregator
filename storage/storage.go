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
*/

// Package storage contains an implementation of interface between Go code and
// (almost any) SQL database like PostgreSQL, SQLite, or MariaDB. An implementation
// named DBStorage is constructed via New function and it is mandatory to call Close
// for any opened connection to database. The storage might be initialized by Init
// method if database schema is empty.
//
// It is possible to configure connection to selected database by using Configuration
// structure. Currently that structure contains two configurable parameter:
//
// Driver - a SQL driver, like "sqlite3", "pq" etc.
// DataSource - specification of data source. The content of this parameter depends on the database used.
package storage

import (
	"database/sql"
	sql_driver "database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Shopify/sarama"
	"github.com/lib/pq"
	_ "github.com/lib/pq" // PostgreSQL database driver
	"github.com/mattn/go-sqlite3"
	_ "github.com/mattn/go-sqlite3" // SQLite database driver
	"github.com/rs/zerolog/log"

	"github.com/RedHatInsights/insights-results-aggregator/metrics"
	"github.com/RedHatInsights/insights-results-aggregator/migration"
	"github.com/RedHatInsights/insights-results-aggregator/types"
)

// Storage represents an interface to almost any database or storage system
type Storage interface {
	Init() error
	Close() error
	ListOfOrgs() ([]types.OrgID, error)
	ListOfClustersForOrg(
		orgID types.OrgID, timeLimit time.Time) ([]types.ClusterName, error,
	)
	ReadReportForCluster(
		orgID types.OrgID, clusterName types.ClusterName) ([]types.RuleOnReport, types.Timestamp, error,
	)
	ReadReportsForClusters(
		clusterNames []types.ClusterName) (map[types.ClusterName]types.ClusterReport, error)
	ReadOrgIDsForClusters(
		clusterNames []types.ClusterName) ([]types.OrgID, error)
	ReadSingleRuleTemplateData(
		orgID types.OrgID, clusterName types.ClusterName, ruleID types.RuleID, errorKey types.ErrorKey,
	) (interface{}, error)
	ReadReportForClusterByClusterName(clusterName types.ClusterName) ([]types.RuleOnReport, types.Timestamp, error)
	GetLatestKafkaOffset() (types.KafkaOffset, error)
	WriteReportForCluster(
		orgID types.OrgID,
		clusterName types.ClusterName,
		report types.ClusterReport,
		rules []types.ReportItem,
		collectedAtTime time.Time,
		kafkaOffset types.KafkaOffset,
	) error
	ReportsCount() (int, error)
	VoteOnRule(
		clusterID types.ClusterName,
		ruleID types.RuleID,
		errorKey types.ErrorKey,
		userID types.UserID,
		userVote types.UserVote,
		voteMessage string,
	) error
	AddOrUpdateFeedbackOnRule(
		clusterID types.ClusterName,
		ruleID types.RuleID,
		errorKey types.ErrorKey,
		userID types.UserID,
		message string,
	) error
	AddFeedbackOnRuleDisable(
		clusterID types.ClusterName,
		ruleID types.RuleID,
		errorKey types.ErrorKey,
		userID types.UserID,
		message string,
	) error
	GetUserFeedbackOnRule(
		clusterID types.ClusterName,
		ruleID types.RuleID,
		errorKey types.ErrorKey,
		userID types.UserID,
	) (*UserFeedbackOnRule, error)
	GetUserFeedbackOnRuleDisable(
		clusterID types.ClusterName, ruleID types.RuleID, userID types.UserID,
	) (*UserFeedbackOnRule, error)
	DeleteReportsForOrg(orgID types.OrgID) error
	DeleteReportsForCluster(clusterName types.ClusterName) error
	ToggleRuleForCluster(
		clusterID types.ClusterName,
		ruleID types.RuleID,
		errorKey types.ErrorKey,
		ruleToggle RuleToggle,
	) error
	GetFromClusterRuleToggle(
		types.ClusterName,
		types.RuleID,
	) (*ClusterRuleToggle, error)
	GetTogglesForRules(
		types.ClusterName,
		[]types.RuleOnReport,
	) (map[types.RuleID]bool, error)
	DeleteFromRuleClusterToggle(
		clusterID types.ClusterName,
		ruleID types.RuleID,
	) error
	GetOrgIDByClusterID(cluster types.ClusterName) (types.OrgID, error)
	WriteConsumerError(msg *sarama.ConsumerMessage, consumerErr error) error
	GetUserFeedbackOnRules(
		clusterID types.ClusterName,
		rulesReport []types.RuleOnReport,
		userID types.UserID,
	) (map[types.RuleID]types.UserVote, error)
	GetUserDisableFeedbackOnRules(
		clusterID types.ClusterName,
		rulesReport []types.RuleOnReport,
		userID types.UserID,
	) (map[types.RuleID]UserFeedbackOnRule, error)
	DoesClusterExist(clusterID types.ClusterName) (bool, error)
}

// DBStorage is an implementation of Storage interface that use selected SQL like database
// like SQLite, PostgreSQL, MariaDB, RDS etc. That implementation is based on the standard
// sql package. It is possible to configure connection via Configuration structure.
// SQLQueriesLog is log for sql queries, default is nil which means nothing is logged
type DBStorage struct {
	connection   *sql.DB
	dbDriverType types.DBDriver
	// clusterLastCheckedDict is a dictionary of timestamps when the clusters were last checked.
	clustersLastChecked map[types.ClusterName]time.Time
}

// New function creates and initializes a new instance of Storage interface
func New(configuration Configuration) (*DBStorage, error) {
	driverType, driverName, dataSource, err := initAndGetDriver(configuration)
	if err != nil {
		return nil, err
	}

	log.Info().Msgf(
		"Making connection to data storage, driver=%s datasource=%s",
		driverName, dataSource,
	)

	connection, err := sql.Open(driverName, dataSource)
	if err != nil {
		log.Error().Err(err).Msg("Can not connect to data storage")
		return nil, err
	}

	return NewFromConnection(connection, driverType), nil
}

// NewFromConnection function creates and initializes a new instance of Storage interface from prepared connection
func NewFromConnection(connection *sql.DB, dbDriverType types.DBDriver) *DBStorage {
	return &DBStorage{
		connection:          connection,
		dbDriverType:        dbDriverType,
		clustersLastChecked: map[types.ClusterName]time.Time{},
	}
}

// initAndGetDriver initializes driver(with logs if logSQLQueries is true),
// checks if it's supported and returns driver type, driver name, dataSource and error
func initAndGetDriver(configuration Configuration) (driverType types.DBDriver, driverName string, dataSource string, err error) {
	var driver sql_driver.Driver
	driverName = configuration.Driver

	switch driverName {
	case "sqlite3":
		driverType = types.DBDriverSQLite3
		driver = &sqlite3.SQLiteDriver{}
		dataSource = configuration.SQLiteDataSource
	case "postgres":
		driverType = types.DBDriverPostgres
		driver = &pq.Driver{}
		dataSource = fmt.Sprintf(
			"postgresql://%v:%v@%v:%v/%v?%v",
			configuration.PGUsername,
			configuration.PGPassword,
			configuration.PGHost,
			configuration.PGPort,
			configuration.PGDBName,
			configuration.PGParams,
		)
	default:
		err = fmt.Errorf("driver %v is not supported", driverName)
		return
	}

	if configuration.LogSQLQueries {
		driverName = InitSQLDriverWithLogs(driver, driverName)
	}

	return
}

// MigrateToLatest migrates the database to the latest available
// migration version. This must be done before an Init() call.
func (storage DBStorage) MigrateToLatest() error {
	if err := migration.InitInfoTable(storage.connection); err != nil {
		return err
	}

	return migration.SetDBVersion(storage.connection, storage.dbDriverType, migration.GetMaxVersion())
}

// Init performs all database initialization
// tasks necessary for further service operation.
func (storage DBStorage) Init() error {
	// Read clusterName:LastChecked dictionary from DB.
	rows, err := storage.connection.Query("SELECT cluster, last_checked_at FROM report;")
	if err != nil {
		return err
	}

	for rows.Next() {
		var (
			clusterName types.ClusterName
			lastChecked time.Time
		)

		if err := rows.Scan(&clusterName, &lastChecked); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				log.Error().Err(closeErr).Msg("Unable to close the DB rows handle")
			}
			return err
		}

		storage.clustersLastChecked[clusterName] = lastChecked
	}

	// Not using defer to close the rows here to:
	// - make errcheck happy (it doesn't like ignoring returned errors),
	// - return a possible error returned by the Close method.
	return rows.Close()
}

// Close method closes the connection to database. Needs to be called at the end of application lifecycle.
func (storage DBStorage) Close() error {
	log.Info().Msg("Closing connection to data storage")
	if storage.connection != nil {
		err := storage.connection.Close()
		if err != nil {
			log.Error().Err(err).Msg("Can not close connection to data storage")
			return err
		}
	}
	return nil
}

// Report represents one (latest) cluster report.
//     Org: organization ID
//     Name: cluster GUID in the following format:
//         c8590f31-e97e-4b85-b506-c45ce1911a12
type Report struct {
	Org        types.OrgID         `json:"org"`
	Name       types.ClusterName   `json:"cluster"`
	Report     types.ClusterReport `json:"report"`
	ReportedAt types.Timestamp     `json:"reported_at"`
}

func closeRows(rows *sql.Rows) {
	_ = rows.Close()
}

// ListOfOrgs reads list of all organizations that have at least one cluster report
func (storage DBStorage) ListOfOrgs() ([]types.OrgID, error) {
	orgs := make([]types.OrgID, 0)

	rows, err := storage.connection.Query("SELECT DISTINCT org_id FROM report ORDER BY org_id;")
	err = types.ConvertDBError(err, nil)
	if err != nil {
		return orgs, err
	}
	defer closeRows(rows)

	for rows.Next() {
		var orgID types.OrgID

		err = rows.Scan(&orgID)
		if err == nil {
			orgs = append(orgs, orgID)
		} else {
			log.Error().Err(err).Msg("ListOfOrgID")
		}
	}
	return orgs, nil
}

// ListOfClustersForOrg reads list of all clusters fro given organization
func (storage DBStorage) ListOfClustersForOrg(orgID types.OrgID, timeLimit time.Time) ([]types.ClusterName, error) {
	clusters := make([]types.ClusterName, 0)

	q := `
		SELECT cluster
		FROM report
		WHERE org_id = $1
		AND reported_at >= $2
		ORDER BY cluster;
	`

	rows, err := storage.connection.Query(q, orgID, timeLimit)

	err = types.ConvertDBError(err, orgID)
	if err != nil {
		return clusters, err
	}
	defer closeRows(rows)

	for rows.Next() {
		var clusterName string

		err = rows.Scan(&clusterName)
		if err == nil {
			clusters = append(clusters, types.ClusterName(clusterName))
		} else {
			log.Error().Err(err).Msg("ListOfClustersForOrg")
		}
	}
	return clusters, nil
}

// GetOrgIDByClusterID reads OrgID for specified cluster
func (storage DBStorage) GetOrgIDByClusterID(cluster types.ClusterName) (types.OrgID, error) {
	row := storage.connection.QueryRow("SELECT org_id FROM report WHERE cluster = $1 ORDER BY org_id;", cluster)

	var orgID uint64
	err := row.Scan(&orgID)
	if err != nil {
		log.Error().Err(err).Msg("GetOrgIDByClusterID")
		return 0, err
	}
	return types.OrgID(orgID), nil
}

// parseTemplateData parses template data and returns a json raw message if it's a json or a string otherwise
func parseTemplateData(templateData []byte) interface{} {
	var templateDataJSON json.RawMessage

	err := json.Unmarshal(templateData, &templateDataJSON)
	if err != nil {
		log.Warn().Err(err).Msgf("unable to parse template data as json")
		return templateData
	}

	return templateDataJSON
}

func parseRuleRows(rows *sql.Rows) ([]types.RuleOnReport, error) {
	report := make([]types.RuleOnReport, 0)

	for rows.Next() {
		var (
			templateDataBytes []byte
			ruleFQDN          types.RuleID
			errorKey          types.ErrorKey
		)

		err := rows.Scan(&templateDataBytes, &ruleFQDN, &errorKey)
		if err != nil {
			log.Error().Err(err).Msg("ReportListForCluster")
			return report, err
		}

		templateData := parseTemplateData(templateDataBytes)
		rule := types.RuleOnReport{
			Module:       ruleFQDN,
			ErrorKey:     errorKey,
			TemplateData: templateData,
		}

		report = append(report, rule)
	}

	return report, nil
}

// constructInClausule is a helper function to construct `in` clausule for SQL
// statement.
func constructInClausule(howMany int) string {
	// construct the `in` clausule in SQL query statement
	inClausule := "$1"
	for i := 2; i <= howMany; i++ {
		inClausule += fmt.Sprintf(",$%d", i)
	}
	return inClausule
}

// argsWithClusterNames is a helper function to construct arguments for SQL
// statement.
func argsWithClusterNames(clusterNames []types.ClusterName) []interface{} {
	// prepare arguments
	args := make([]interface{}, len(clusterNames))

	for i, clusterName := range clusterNames {
		args[i] = clusterName
	}
	return args
}

// ReadOrgIDsForClusters read organization IDs for given list of cluster names.
func (storage DBStorage) ReadOrgIDsForClusters(clusterNames []types.ClusterName) ([]types.OrgID, error) {
	// stub for return value
	ids := make([]types.OrgID, 0)

	// prepare arguments
	args := argsWithClusterNames(clusterNames)

	// construct the `in` clausule in SQL query statement
	inClausule := constructInClausule(len(clusterNames))

	// disable "G202 (CWE-89): SQL string concatenation"
	// #nosec G202
	query := "SELECT DISTINCT org_id FROM report WHERE cluster in (" + inClausule + ");"

	// select results from the database
	rows, err := storage.connection.Query(query, args...)
	if err != nil {
		log.Error().Err(err).Msg("query to get org ids")
		return ids, err
	}

	// process results returned from database
	for rows.Next() {
		var orgID types.OrgID

		err := rows.Scan(&orgID)
		if err != nil {
			log.Error().Err(err).Msg("read one org id")
			return ids, err
		}

		ids = append(ids, orgID)
	}

	// everything seems ok -> return ids
	return ids, nil
}

// ReadReportsForClusters function reads reports for given list of cluster
// names.
func (storage DBStorage) ReadReportsForClusters(clusterNames []types.ClusterName) (map[types.ClusterName]types.ClusterReport, error) {
	// stub for return value
	reports := make(map[types.ClusterName]types.ClusterReport)

	// prepare arguments
	args := argsWithClusterNames(clusterNames)

	// construct the `in` clausule in SQL query statement
	inClausule := constructInClausule(len(clusterNames))

	// disable "G202 (CWE-89): SQL string concatenation"
	// #nosec G202
	query := "SELECT cluster, report FROM report WHERE cluster in (" + inClausule + ");"

	// select results from the database
	rows, err := storage.connection.Query(query, args...)
	if err != nil {
		return reports, err
	}

	// process results returned from database
	for rows.Next() {
		// convert into requested type
		var (
			clusterName   types.ClusterName
			clusterReport types.ClusterReport
		)

		err := rows.Scan(&clusterName, &clusterReport)
		if err != nil {
			log.Error().Err(err).Msg("ReadReportsForClusters")
			return reports, err
		}

		reports[clusterName] = clusterReport
	}

	// everything seems ok -> return reports
	return reports, nil
}

// ReadReportForCluster reads result (health status) for selected cluster
func (storage DBStorage) ReadReportForCluster(
	orgID types.OrgID, clusterName types.ClusterName,
) ([]types.RuleOnReport, types.Timestamp, error) {
	var lastChecked time.Time
	report := make([]types.RuleOnReport, 0)

	err := storage.connection.QueryRow(
		"SELECT last_checked_at FROM report WHERE org_id = $1 AND cluster = $2;", orgID, clusterName,
	).Scan(&lastChecked)
	err = types.ConvertDBError(err, []interface{}{orgID, clusterName})
	if err != nil {
		return report, types.Timestamp(lastChecked.UTC().Format(time.RFC3339)), err
	}

	rows, err := storage.connection.Query(
		"SELECT template_data, rule_fqdn, error_key FROM rule_hit WHERE org_id = $1 AND cluster_id = $2;", orgID, clusterName,
	)

	err = types.ConvertDBError(err, []interface{}{orgID, clusterName})
	if err != nil {
		return report, types.Timestamp(lastChecked.UTC().Format(time.RFC3339)), err
	}

	report, err = parseRuleRows(rows)

	return report, types.Timestamp(lastChecked.UTC().Format(time.RFC3339)), err
}

// ReadSingleRuleTemplateData reads template data for a single rule
func (storage DBStorage) ReadSingleRuleTemplateData(
	orgID types.OrgID, clusterName types.ClusterName, ruleID types.RuleID, errorKey types.ErrorKey,
) (interface{}, error) {
	var templateDataBytes []byte

	err := storage.connection.QueryRow(`
		SELECT template_data FROM rule_hit
		WHERE org_id = $1 AND cluster_id = $2 AND rule_fqdn = $3 AND error_key = $4;
	`,
		orgID,
		clusterName,
		ruleID,
		errorKey,
	).Scan(&templateDataBytes)
	err = types.ConvertDBError(err, []interface{}{orgID, clusterName, ruleID, errorKey})

	return parseTemplateData(templateDataBytes), err
}

// ReadReportForClusterByClusterName reads result (health status) for selected cluster for given organization
func (storage DBStorage) ReadReportForClusterByClusterName(
	clusterName types.ClusterName,
) ([]types.RuleOnReport, types.Timestamp, error) {
	report := make([]types.RuleOnReport, 0)
	var lastChecked time.Time

	err := storage.connection.QueryRow(
		"SELECT last_checked_at FROM report WHERE cluster = $1;", clusterName,
	).Scan(&lastChecked)

	switch {
	case err == sql.ErrNoRows:
		return report, "", &types.ItemNotFoundError{
			ItemID: fmt.Sprintf("%v", clusterName),
		}
	case err != nil:
		return report, "", err
	}

	rows, err := storage.connection.Query(
		"SELECT template_data, rule_fqdn, error_key FROM rule_hit WHERE cluster_id = $1;", clusterName,
	)

	if err != nil {
		return report, types.Timestamp(lastChecked.UTC().Format(time.RFC3339)), err
	}

	report, err = parseRuleRows(rows)

	return report, types.Timestamp(lastChecked.UTC().Format(time.RFC3339)), err
}

// GetLatestKafkaOffset returns latest kafka offset from report table
func (storage DBStorage) GetLatestKafkaOffset() (types.KafkaOffset, error) {
	var offset types.KafkaOffset
	err := storage.connection.QueryRow("SELECT COALESCE(MAX(kafka_offset), 0) FROM report;").Scan(&offset)
	return offset, err
}

func (storage DBStorage) getReportUpsertQuery() string {
	if storage.dbDriverType == types.DBDriverSQLite3 {
		return `
			INSERT OR REPLACE INTO report(org_id, cluster, report, reported_at, last_checked_at, kafka_offset)
			VALUES ($1, $2, $3, $4, $5, $6)
		`
	}

	return `
		INSERT INTO report(org_id, cluster, report, reported_at, last_checked_at, kafka_offset)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (cluster)
		DO UPDATE SET org_id = $1, report = $3, reported_at = $4, last_checked_at = $5, kafka_offset = $6
	`
}

func (storage DBStorage) getRuleHitUpsertQuery() string {
	if storage.dbDriverType == types.DBDriverSQLite3 {
		return `
			INSERT OR REPLACE INTO rule_hit(org_id, cluster_id, rule_fqdn, error_key, template_data)
			VALUES ($1, $2, $3, $4, $5)
		`
	}

	return `
		INSERT INTO rule_hit(org_id, cluster_id, rule_fqdn, error_key, template_data)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, cluster_id, rule_fqdn, error_key)
		DO UPDATE SET template_data = $4
	`
}

func (storage DBStorage) updateReport(
	tx *sql.Tx,
	orgID types.OrgID,
	clusterName types.ClusterName,
	report types.ClusterReport,
	rules []types.ReportItem,
	lastCheckedTime time.Time,
	kafkaOffset types.KafkaOffset,
) error {
	// Get the UPSERT query for writing a report into the database.
	reportUpsertQuery := storage.getReportUpsertQuery()

	// Get the UPSERT query for writing a rule into the database.
	ruleUpsertQuery := storage.getRuleHitUpsertQuery()

	deleteQuery := "DELETE FROM rule_hit WHERE org_id = $1 AND cluster_id = $2;"
	_, err := tx.Exec(deleteQuery, orgID, clusterName)
	if err != nil {
		log.Err(err).Msgf("Unable to remove previous cluster reports (org: %v, cluster: %v)", orgID, clusterName)
		return err
	}

	// Perform the report upsert.
	reportedAtTime := time.Now()

	for _, rule := range rules {
		_, err = tx.Exec(ruleUpsertQuery, orgID, clusterName, rule.Module, rule.ErrorKey, string(rule.TemplateData))
		if err != nil {
			log.Err(err).Msgf("Unable to upsert the cluster report rules (org: %v, cluster: %v, rule: %v|%v)",
				orgID, clusterName, rule.Module, rule.ErrorKey,
			)
			return err
		}
	}

	_, err = tx.Exec(reportUpsertQuery, orgID, clusterName, report, reportedAtTime, lastCheckedTime, kafkaOffset)
	if err != nil {
		log.Err(err).Msgf("Unable to upsert the cluster report (org: %v, cluster: %v)", orgID, clusterName)
		return err
	}

	return nil
}

// WriteReportForCluster writes result (health status) for selected cluster for given organization
func (storage DBStorage) WriteReportForCluster(
	orgID types.OrgID,
	clusterName types.ClusterName,
	report types.ClusterReport,
	rules []types.ReportItem,
	lastCheckedTime time.Time,
	kafkaOffset types.KafkaOffset,
) error {
	// Skip writing the report if it isn't newer than a report
	// that is already in the database for the same cluster.
	if oldLastChecked, exists := storage.clustersLastChecked[clusterName]; exists && !lastCheckedTime.After(oldLastChecked) {
		return types.ErrOldReport
	}

	if storage.dbDriverType != types.DBDriverSQLite3 && storage.dbDriverType != types.DBDriverPostgres {
		return fmt.Errorf("writing report with DB %v is not supported", storage.dbDriverType)
	}

	// Begin a new transaction.
	tx, err := storage.connection.Begin()
	if err != nil {
		return err
	}

	err = func(tx *sql.Tx) error {

		// Check if there is a more recent report for the cluster already in the database.
		rows, err := tx.Query(
			"SELECT last_checked_at FROM report WHERE org_id = $1 AND cluster = $2 AND last_checked_at > $3;",
			orgID, clusterName, lastCheckedTime)
		err = types.ConvertDBError(err, []interface{}{orgID, clusterName})
		if err != nil {
			log.Error().Err(err).Msg("Unable to look up the most recent report in the database")
			return err
		}

		defer closeRows(rows)

		// If there is one, print a warning and discard the report (don't update it).
		if rows.Next() {
			log.Warn().Msgf("Database already contains report for organization %d and cluster name %s more recent than %v",
				orgID, clusterName, lastCheckedTime)
			return nil
		}

		err = storage.updateReport(tx, orgID, clusterName, report, rules, lastCheckedTime, kafkaOffset)
		if err != nil {
			return err
		}

		storage.clustersLastChecked[clusterName] = lastCheckedTime
		metrics.WrittenReports.Inc()

		return nil
	}(tx)

	finishTransaction(tx, err)

	return err
}

// finishTransaction finishes the transaction depending on err. err == nil -> commit, err != nil -> rollback
func finishTransaction(tx *sql.Tx, err error) {
	if err != nil {
		rollbackError := tx.Rollback()
		if rollbackError != nil {
			log.Err(rollbackError).Msgf("error when trying to rollback a transaction")
		}
	} else {
		commitError := tx.Commit()
		if commitError != nil {
			log.Err(commitError).Msgf("error when trying to commit a transaction")
		}
	}
}

// ReportsCount reads number of all records stored in database
func (storage DBStorage) ReportsCount() (int, error) {
	count := -1
	err := storage.connection.QueryRow("SELECT count(*) FROM report;").Scan(&count)
	err = types.ConvertDBError(err, nil)

	return count, err
}

// DeleteReportsForOrg deletes all reports related to the specified organization from the storage.
func (storage DBStorage) DeleteReportsForOrg(orgID types.OrgID) error {
	_, err := storage.connection.Exec("DELETE FROM report WHERE org_id = $1;", orgID)
	return err
}

// DeleteReportsForCluster deletes all reports related to the specified cluster from the storage.
func (storage DBStorage) DeleteReportsForCluster(clusterName types.ClusterName) error {
	_, err := storage.connection.Exec("DELETE FROM report WHERE cluster = $1;", clusterName)
	return err
}

// GetConnection returns db connection(useful for testing)
func (storage DBStorage) GetConnection() *sql.DB {
	return storage.connection
}

// WriteConsumerError writes a report about a consumer error into the storage.
func (storage DBStorage) WriteConsumerError(msg *sarama.ConsumerMessage, consumerErr error) error {
	_, err := storage.connection.Exec(`
		INSERT INTO consumer_error (topic, partition, topic_offset, key, produced_at, consumed_at, message, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		msg.Topic, msg.Partition, msg.Offset, msg.Key, msg.Timestamp, time.Now().UTC(), msg.Value, consumerErr.Error())

	return err
}

// GetDBDriverType returns db driver type
func (storage DBStorage) GetDBDriverType() types.DBDriver {
	return storage.dbDriverType
}

// DoesClusterExist checks if cluster with this id exists
func (storage DBStorage) DoesClusterExist(clusterID types.ClusterName) (bool, error) {
	err := storage.connection.QueryRow(
		"SELECT cluster FROM report WHERE cluster = $1", clusterID,
	).Scan(&clusterID)
	if err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return true, nil
}
