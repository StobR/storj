// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package satellitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zeebo/errs"

	"storj.io/common/memory"
	"storj.io/common/pb"
	"storj.io/common/uuid"
	"storj.io/private/dbutil"
	"storj.io/private/dbutil/pgutil"
	"storj.io/storj/satellite/accounting"
	"storj.io/storj/satellite/metabase"
	"storj.io/storj/satellite/orders"
	"storj.io/storj/satellite/satellitedb/dbx"
)

// ensure that ProjectAccounting implements accounting.ProjectAccounting.
var _ accounting.ProjectAccounting = (*ProjectAccounting)(nil)

var allocatedExpirationInDays = 2

// ProjectAccounting implements the accounting/db ProjectAccounting interface.
type ProjectAccounting struct {
	db *satelliteDB
}

// SaveTallies saves the latest bucket info.
func (db *ProjectAccounting) SaveTallies(ctx context.Context, intervalStart time.Time, bucketTallies map[metabase.BucketLocation]*accounting.BucketTally) (err error) {
	defer mon.Task()(&ctx)(&err)
	if len(bucketTallies) == 0 {
		return nil
	}
	var bucketNames, projectIDs [][]byte
	var totalBytes, metadataSizes []int64
	var totalSegments, objectCounts []int64
	for _, info := range bucketTallies {
		bucketNames = append(bucketNames, []byte(info.BucketName))
		projectIDs = append(projectIDs, info.ProjectID[:])
		totalBytes = append(totalBytes, info.TotalBytes)
		totalSegments = append(totalSegments, info.TotalSegments)
		objectCounts = append(objectCounts, info.ObjectCount)
		metadataSizes = append(metadataSizes, info.MetadataSize)
	}
	_, err = db.db.DB.ExecContext(ctx, db.db.Rebind(`
		INSERT INTO bucket_storage_tallies (
			interval_start,
			bucket_name, project_id,
			total_bytes, inline, remote,
			total_segments_count, remote_segments_count, inline_segments_count,
			object_count, metadata_size)
		SELECT
			$1,
			unnest($2::bytea[]), unnest($3::bytea[]),
			unnest($4::int8[]), $5, $6,
			unnest($7::int8[]), $8, $9,
			unnest($10::int8[]), unnest($11::int8[])`),
		intervalStart,
		pgutil.ByteaArray(bucketNames), pgutil.ByteaArray(projectIDs),
		pgutil.Int8Array(totalBytes), 0, 0,
		pgutil.Int8Array(totalSegments), 0, 0,
		pgutil.Int8Array(objectCounts), pgutil.Int8Array(metadataSizes))

	return Error.Wrap(err)
}

// GetTallies saves the latest bucket info.
func (db *ProjectAccounting) GetTallies(ctx context.Context) (tallies []accounting.BucketTally, err error) {
	defer mon.Task()(&ctx)(&err)

	dbxTallies, err := db.db.All_BucketStorageTally(ctx)
	if err != nil {
		return nil, Error.Wrap(err)
	}

	for _, dbxTally := range dbxTallies {
		projectID, err := uuid.FromBytes(dbxTally.ProjectId)
		if err != nil {
			return nil, Error.Wrap(err)
		}

		totalBytes := dbxTally.TotalBytes
		if totalBytes == 0 {
			totalBytes = dbxTally.Inline + dbxTally.Remote
		}

		totalSegments := dbxTally.TotalSegmentsCount
		if totalSegments == 0 {
			totalSegments = dbxTally.InlineSegmentsCount + dbxTally.RemoteSegmentsCount
		}

		tallies = append(tallies, accounting.BucketTally{
			BucketLocation: metabase.BucketLocation{
				ProjectID:  projectID,
				BucketName: string(dbxTally.BucketName),
			},
			ObjectCount:   int64(dbxTally.ObjectCount),
			TotalSegments: int64(totalSegments),
			TotalBytes:    int64(totalBytes),
			MetadataSize:  int64(dbxTally.MetadataSize),
		})
	}

	return tallies, nil
}

// CreateStorageTally creates a record in the bucket_storage_tallies accounting table.
func (db *ProjectAccounting) CreateStorageTally(ctx context.Context, tally accounting.BucketStorageTally) (err error) {
	defer mon.Task()(&ctx)(&err)

	_, err = db.db.DB.ExecContext(ctx, db.db.Rebind(`
		INSERT INTO bucket_storage_tallies (
			interval_start,
			bucket_name, project_id,
			total_bytes, inline, remote,
			total_segments_count, remote_segments_count, inline_segments_count,
			object_count, metadata_size)
		VALUES (
			?,
			?, ?,
			?, ?, ?,
			?, ?, ?,
			?, ?
		)`), tally.IntervalStart,
		[]byte(tally.BucketName), tally.ProjectID,
		tally.TotalBytes, 0, 0,
		tally.TotalSegmentCount, 0, 0,
		tally.ObjectCount, tally.MetadataSize,
	)

	return Error.Wrap(err)
}

// GetAllocatedBandwidthTotal returns the sum of GET bandwidth usage allocated for a projectID for a time frame.
func (db *ProjectAccounting) GetAllocatedBandwidthTotal(ctx context.Context, projectID uuid.UUID, from time.Time) (_ int64, err error) {
	defer mon.Task()(&ctx)(&err)
	var sum *int64
	query := `SELECT SUM(allocated) FROM bucket_bandwidth_rollups WHERE project_id = ? AND action = ? AND interval_start >= ?;`
	err = db.db.QueryRow(ctx, db.db.Rebind(query), projectID[:], pb.PieceAction_GET, from.UTC()).Scan(&sum)
	if errors.Is(err, sql.ErrNoRows) || sum == nil {
		return 0, nil
	}

	return *sum, err
}

// GetProjectBandwidth returns the used bandwidth (settled or allocated) for the specified year, month and day.
func (db *ProjectAccounting) GetProjectBandwidth(ctx context.Context, projectID uuid.UUID, year int, month time.Month, day int, asOfSystemInterval time.Duration) (_ int64, err error) {
	defer mon.Task()(&ctx)(&err)
	var egress *int64

	startOfMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)

	var expiredSince time.Time
	if day < allocatedExpirationInDays {
		expiredSince = startOfMonth
	} else {
		expiredSince = time.Date(year, month, day-allocatedExpirationInDays, 0, 0, 0, 0, time.UTC)
	}
	periodEnd := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)

	query := `WITH egress AS (
					SELECT
						CASE WHEN interval_day < ?
							THEN egress_settled
							ELSE egress_allocated
						END AS amount
					FROM project_bandwidth_daily_rollups
					WHERE project_id = ? AND interval_day >= ? AND interval_day < ?
				) SELECT sum(amount) FROM egress` + db.db.impl.AsOfSystemInterval(asOfSystemInterval)
	err = db.db.QueryRow(ctx, db.db.Rebind(query), expiredSince, projectID[:], startOfMonth, periodEnd).Scan(&egress)
	if errors.Is(err, sql.ErrNoRows) || egress == nil {
		return 0, nil
	}

	return *egress, err
}

// GetProjectDailyBandwidth returns project bandwidth (allocated and settled) for the specified day.
func (db *ProjectAccounting) GetProjectDailyBandwidth(ctx context.Context, projectID uuid.UUID, year int, month time.Month, day int) (allocated int64, settled int64, err error) {
	defer mon.Task()(&ctx)(&err)

	interval := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)

	query := `SELECT egress_allocated, egress_settled FROM project_bandwidth_daily_rollups WHERE project_id = ? AND interval_day = ?;`
	err = db.db.QueryRow(ctx, db.db.Rebind(query), projectID[:], interval).Scan(&allocated, &settled)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}

	return allocated, settled, err
}

// DeleteProjectBandwidthBefore deletes project bandwidth rollups before the given time.
func (db *ProjectAccounting) DeleteProjectBandwidthBefore(ctx context.Context, before time.Time) (err error) {
	defer mon.Task()(&ctx)(&err)

	_, err = db.db.DB.ExecContext(ctx, db.db.Rebind("DELETE FROM project_bandwidth_daily_rollups WHERE interval_day < ?"), before)

	return err
}

// UpdateProjectUsageLimit updates project usage limit.
func (db *ProjectAccounting) UpdateProjectUsageLimit(ctx context.Context, projectID uuid.UUID, limit memory.Size) (err error) {
	defer mon.Task()(&ctx)(&err)

	_, err = db.db.Update_Project_By_Id(ctx,
		dbx.Project_Id(projectID[:]),
		dbx.Project_Update_Fields{
			UsageLimit: dbx.Project_UsageLimit(limit.Int64()),
		},
	)

	return err
}

// UpdateProjectBandwidthLimit updates project bandwidth limit.
func (db *ProjectAccounting) UpdateProjectBandwidthLimit(ctx context.Context, projectID uuid.UUID, limit memory.Size) (err error) {
	defer mon.Task()(&ctx)(&err)

	_, err = db.db.Update_Project_By_Id(ctx,
		dbx.Project_Id(projectID[:]),
		dbx.Project_Update_Fields{
			BandwidthLimit: dbx.Project_BandwidthLimit(limit.Int64()),
		},
	)

	return err
}

// GetProjectStorageLimit returns project storage usage limit.
func (db *ProjectAccounting) GetProjectStorageLimit(ctx context.Context, projectID uuid.UUID) (_ *int64, err error) {
	defer mon.Task()(&ctx)(&err)

	row, err := db.db.Get_Project_UsageLimit_By_Id(ctx,
		dbx.Project_Id(projectID[:]),
	)
	if err != nil {
		return nil, err
	}

	return row.UsageLimit, nil
}

// GetProjectBandwidthLimit returns project bandwidth usage limit.
func (db *ProjectAccounting) GetProjectBandwidthLimit(ctx context.Context, projectID uuid.UUID) (_ *int64, err error) {
	defer mon.Task()(&ctx)(&err)

	row, err := db.db.Get_Project_BandwidthLimit_By_Id(ctx,
		dbx.Project_Id(projectID[:]),
	)
	if err != nil {
		return nil, err
	}

	return row.BandwidthLimit, nil
}

// GetProjectTotal retrieves project usage for a given period.
func (db *ProjectAccounting) GetProjectTotal(ctx context.Context, projectID uuid.UUID, since, before time.Time) (usage *accounting.ProjectUsage, err error) {
	defer mon.Task()(&ctx)(&err)
	since = timeTruncateDown(since)
	bucketNames, err := db.getBucketsSinceAndBefore(ctx, projectID, since, before)
	if err != nil {
		return nil, err
	}

	storageQuery := db.db.Rebind(`
		SELECT
			bucket_storage_tallies.interval_start,
			bucket_storage_tallies.total_bytes,
			bucket_storage_tallies.inline,
			bucket_storage_tallies.remote,
			bucket_storage_tallies.object_count
		FROM
			bucket_storage_tallies
		WHERE
			bucket_storage_tallies.project_id = ? AND
			bucket_storage_tallies.bucket_name = ? AND
			bucket_storage_tallies.interval_start >= ? AND
			bucket_storage_tallies.interval_start <= ?
		ORDER BY bucket_storage_tallies.interval_start DESC
	`)

	bucketsTallies := make(map[string][]*accounting.BucketStorageTally)

	for _, bucket := range bucketNames {
		storageTallies := make([]*accounting.BucketStorageTally, 0)

		storageTalliesRows, err := db.db.QueryContext(ctx, storageQuery, projectID[:], []byte(bucket), since, before)
		if err != nil {
			return nil, err
		}
		// generating tallies for each bucket name.
		for storageTalliesRows.Next() {
			tally := accounting.BucketStorageTally{}

			var inline, remote int64
			err = storageTalliesRows.Scan(&tally.IntervalStart, &tally.TotalBytes, &inline, &remote, &tally.ObjectCount)
			if err != nil {
				return nil, errs.Combine(err, storageTalliesRows.Close())
			}
			if tally.TotalBytes == 0 {
				tally.TotalBytes = inline + remote
			}

			tally.BucketName = bucket
			storageTallies = append(storageTallies, &tally)
		}

		err = errs.Combine(storageTalliesRows.Err(), storageTalliesRows.Close())
		if err != nil {
			return nil, err
		}

		bucketsTallies[bucket] = storageTallies
	}

	totalEgress, err := db.getTotalEgress(ctx, projectID, since, before)
	if err != nil {
		return nil, err
	}

	usage = new(accounting.ProjectUsage)
	usage.Egress = memory.Size(totalEgress).Int64()
	// sum up storage and objects
	for _, tallies := range bucketsTallies {
		for i := len(tallies) - 1; i > 0; i-- {
			current := (tallies)[i]
			hours := (tallies)[i-1].IntervalStart.Sub(current.IntervalStart).Hours()
			usage.Storage += memory.Size(current.Bytes()).Float64() * hours
			usage.ObjectCount += float64(current.ObjectCount) * hours
		}
	}

	usage.Since = since
	usage.Before = before
	return usage, nil
}

// getTotalEgress returns total egress (settled + inline) of each bucket_bandwidth_rollup
// in selected time period, project id.
// only process PieceAction_GET.
func (db *ProjectAccounting) getTotalEgress(ctx context.Context, projectID uuid.UUID, since, before time.Time) (totalEgress int64, err error) {
	totalEgressQuery := db.db.Rebind(`
		SELECT
			COALESCE(SUM(settled) + SUM(inline), 0)
		FROM
			bucket_bandwidth_rollups
		WHERE
			project_id = ? AND
			interval_start >= ? AND
			interval_start <= ? AND
			action = ?;
	`)

	totalEgressRow := db.db.QueryRowContext(ctx, totalEgressQuery, projectID[:], since, before, pb.PieceAction_GET)

	err = totalEgressRow.Scan(&totalEgress)

	return totalEgress, err
}

// GetBucketUsageRollups retrieves summed usage rollups for every bucket of particular project for a given period.
func (db *ProjectAccounting) GetBucketUsageRollups(ctx context.Context, projectID uuid.UUID, since, before time.Time) (_ []accounting.BucketUsageRollup, err error) {
	defer mon.Task()(&ctx)(&err)
	since = timeTruncateDown(since.UTC())
	before = before.UTC()

	buckets, err := db.getBucketsSinceAndBefore(ctx, projectID, since, before)
	if err != nil {
		return nil, err
	}

	roullupsQuery := db.db.Rebind(`SELECT SUM(settled), SUM(inline), action
		FROM bucket_bandwidth_rollups
		WHERE project_id = ? AND bucket_name = ? AND interval_start >= ? AND interval_start <= ?
		GROUP BY action`)

	// TODO: should be optimized
	storageQuery := db.db.All_BucketStorageTally_By_ProjectId_And_BucketName_And_IntervalStart_GreaterOrEqual_And_IntervalStart_LessOrEqual_OrderBy_Desc_IntervalStart

	var bucketUsageRollups []accounting.BucketUsageRollup
	for _, bucket := range buckets {
		err := func() error {
			bucketRollup := accounting.BucketUsageRollup{
				ProjectID:  projectID,
				BucketName: []byte(bucket),
				Since:      since,
				Before:     before,
			}

			// get bucket_bandwidth_rollups
			rollupsRows, err := db.db.QueryContext(ctx, roullupsQuery, projectID[:], []byte(bucket), since, before)
			if err != nil {
				return err
			}
			defer func() { err = errs.Combine(err, rollupsRows.Close()) }()

			// fill egress
			for rollupsRows.Next() {
				var action pb.PieceAction
				var settled, inline int64

				err = rollupsRows.Scan(&settled, &inline, &action)
				if err != nil {
					return err
				}

				switch action {
				case pb.PieceAction_GET:
					bucketRollup.GetEgress += memory.Size(settled + inline).GB()
				case pb.PieceAction_GET_AUDIT:
					bucketRollup.AuditEgress += memory.Size(settled + inline).GB()
				case pb.PieceAction_GET_REPAIR:
					bucketRollup.RepairEgress += memory.Size(settled + inline).GB()
				default:
					continue
				}
			}
			if err := rollupsRows.Err(); err != nil {
				return err
			}

			bucketStorageTallies, err := storageQuery(ctx,
				dbx.BucketStorageTally_ProjectId(projectID[:]),
				dbx.BucketStorageTally_BucketName([]byte(bucket)),
				dbx.BucketStorageTally_IntervalStart(since),
				dbx.BucketStorageTally_IntervalStart(before))

			if err != nil {
				return err
			}

			// fill metadata, objects and stored data
			// hours calculated from previous tallies,
			// so we skip the most recent one
			for i := len(bucketStorageTallies) - 1; i > 0; i-- {
				current := bucketStorageTallies[i]

				hours := bucketStorageTallies[i-1].IntervalStart.Sub(current.IntervalStart).Hours()

				if current.TotalBytes > 0 {
					bucketRollup.TotalStoredData += memory.Size(current.TotalBytes).GB() * hours
				} else {
					bucketRollup.TotalStoredData += memory.Size(current.Remote+current.Inline).GB() * hours
				}
				bucketRollup.MetadataSize += memory.Size(current.MetadataSize).GB() * hours
				if current.TotalSegmentsCount > 0 {
					bucketRollup.TotalSegments += float64(current.TotalSegmentsCount) * hours
				} else {
					bucketRollup.TotalSegments += float64(current.RemoteSegmentsCount+current.InlineSegmentsCount) * hours
				}
				bucketRollup.ObjectCount += float64(current.ObjectCount) * hours
			}

			bucketUsageRollups = append(bucketUsageRollups, bucketRollup)
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}

	return bucketUsageRollups, nil
}

// prefixIncrement returns the lexicographically lowest byte string which is
// greater than origPrefix and does not have origPrefix as a prefix. If no such
// byte string exists (origPrefix is empty, or origPrefix contains only 0xff
// bytes), returns false for ok.
//
// examples: prefixIncrement([]byte("abc"))          -> ([]byte("abd", true)
//           prefixIncrement([]byte("ab\xff\xff"))   -> ([]byte("ac", true)
//           prefixIncrement([]byte(""))             -> (nil, false)
//           prefixIncrement([]byte("\x00"))         -> ([]byte("\x01", true)
//           prefixIncrement([]byte("\xff\xff\xff")) -> (nil, false)
//
func prefixIncrement(origPrefix []byte) (incremented []byte, ok bool) {
	incremented = make([]byte, len(origPrefix))
	copy(incremented, origPrefix)
	i := len(incremented) - 1
	for i >= 0 {
		if incremented[i] != 0xff {
			incremented[i]++
			return incremented[:i+1], true
		}
		i--
	}

	// there is no byte string which is greater than origPrefix and which does
	// not have origPrefix as a prefix.
	return nil, false
}

// prefixMatch creates a SQL expression which
// will evaluate to true if and only if the value of expr starts with the value
// of prefix.
//
// Returns also a slice of arguments that should be passed to the corresponding
// db.Query* or db.Exec* to fill in parameters in the returned SQL expression.
//
// The returned SQL expression needs to be passed through Rebind(), as it uses
// `?` markers instead of `$N`, because we don't know what N we would need to
// use.
func (db *ProjectAccounting) prefixMatch(expr string, prefix []byte) (string, []byte, error) {
	incrementedPrefix, ok := prefixIncrement(prefix)
	switch db.db.impl {
	case dbutil.Postgres:
		if !ok {
			return fmt.Sprintf(`(%s >= ?)`, expr), nil, nil
		}
		return fmt.Sprintf(`(%s >= ? AND %s < ?)`, expr, expr), incrementedPrefix, nil
	case dbutil.Cockroach:
		if !ok {
			return fmt.Sprintf(`(%s >= ?:::BYTEA)`, expr), nil, nil
		}
		return fmt.Sprintf(`(%s >= ?:::BYTEA AND %s < ?:::BYTEA)`, expr, expr), incrementedPrefix, nil
	default:
		return "", nil, errs.New("unhandled database: %v", db.db.driver)
	}

}

// GetBucketTotals retrieves bucket usage totals for period of time.
func (db *ProjectAccounting) GetBucketTotals(ctx context.Context, projectID uuid.UUID, cursor accounting.BucketUsageCursor, since, before time.Time) (_ *accounting.BucketUsagePage, err error) {
	defer mon.Task()(&ctx)(&err)
	since = timeTruncateDown(since)
	bucketPrefix := []byte(cursor.Search)

	if cursor.Limit > 50 {
		cursor.Limit = 50
	}
	if cursor.Page == 0 {
		return nil, errs.New("page can not be 0")
	}

	page := &accounting.BucketUsagePage{
		Search: cursor.Search,
		Limit:  cursor.Limit,
		Offset: uint64((cursor.Page - 1) * cursor.Limit),
	}

	bucketNameRange, incrPrefix, err := db.prefixMatch("name", bucketPrefix)
	if err != nil {
		return nil, err
	}
	countQuery := db.db.Rebind(`SELECT COUNT(name) FROM bucket_metainfos
	WHERE project_id = ? AND ` + bucketNameRange)

	args := []interface{}{
		projectID[:],
		bucketPrefix,
	}
	if incrPrefix != nil {
		args = append(args, incrPrefix)
	}

	countRow := db.db.QueryRowContext(ctx, countQuery, args...)

	err = countRow.Scan(&page.TotalCount)
	if err != nil {
		return nil, err
	}

	if page.TotalCount == 0 {
		return page, nil
	}
	if page.Offset > page.TotalCount-1 {
		return nil, errs.New("page is out of range")
	}

	var buckets []string
	bucketsQuery := db.db.Rebind(`SELECT name FROM bucket_metainfos
	WHERE project_id = ? AND ` + bucketNameRange + `ORDER BY name ASC LIMIT ? OFFSET ?`)

	args = []interface{}{
		projectID[:],
		bucketPrefix,
	}
	if incrPrefix != nil {
		args = append(args, incrPrefix)
	}
	args = append(args, page.Limit, page.Offset)

	bucketRows, err := db.db.QueryContext(ctx, bucketsQuery, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errs.Combine(err, bucketRows.Close()) }()

	for bucketRows.Next() {
		var bucket string
		err = bucketRows.Scan(&bucket)
		if err != nil {
			return nil, err
		}

		buckets = append(buckets, bucket)
	}
	if err := bucketRows.Err(); err != nil {
		return nil, err
	}

	rollupsQuery := db.db.Rebind(`SELECT COALESCE(SUM(settled) + SUM(inline), 0)
		FROM bucket_bandwidth_rollups
		WHERE project_id = ? AND bucket_name = ? AND interval_start >= ? AND interval_start <= ? AND action = ?`)

	storageQuery := db.db.Rebind(`SELECT total_bytes, inline, remote, object_count
		FROM bucket_storage_tallies
		WHERE project_id = ? AND bucket_name = ? AND interval_start >= ? AND interval_start <= ?
		ORDER BY interval_start DESC
		LIMIT 1`)

	var bucketUsages []accounting.BucketUsage
	for _, bucket := range buckets {
		bucketUsage := accounting.BucketUsage{
			ProjectID:  projectID,
			BucketName: bucket,
			Since:      since,
			Before:     before,
		}

		// get bucket_bandwidth_rollups
		rollupRow := db.db.QueryRowContext(ctx, rollupsQuery, projectID[:], []byte(bucket), since, before, pb.PieceAction_GET)

		var egress int64
		err = rollupRow.Scan(&egress)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}

		bucketUsage.Egress = memory.Size(egress).GB()

		storageRow := db.db.QueryRowContext(ctx, storageQuery, projectID[:], []byte(bucket), since, before)

		var tally accounting.BucketStorageTally
		var inline, remote int64
		err = storageRow.Scan(&tally.TotalBytes, &inline, &remote, &tally.ObjectCount)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}

		if tally.TotalBytes == 0 {
			tally.TotalBytes = inline + remote
		}

		// fill storage and object count
		bucketUsage.Storage = memory.Size(tally.Bytes()).GB()
		bucketUsage.ObjectCount = tally.ObjectCount

		bucketUsages = append(bucketUsages, bucketUsage)
	}

	page.PageCount = uint(page.TotalCount / uint64(cursor.Limit))
	if page.TotalCount%uint64(cursor.Limit) != 0 {
		page.PageCount++
	}

	page.BucketUsages = bucketUsages
	page.CurrentPage = cursor.Page
	return page, nil
}

// ArchiveRollupsBefore archives rollups older than a given time.
func (db *ProjectAccounting) ArchiveRollupsBefore(ctx context.Context, before time.Time, batchSize int) (archivedCount int, err error) {
	defer mon.Task()(&ctx)(&err)

	if batchSize <= 0 {
		return 0, nil
	}

	switch db.db.impl {
	case dbutil.Cockroach:

		// We operate one action at a time, because we have an index on `(action, interval_start, project_id)`.
		for action := range pb.PieceAction_name {
			count, err := db.archiveRollupsBeforeByAction(ctx, action, before, batchSize)
			archivedCount += count
			if err != nil {
				return archivedCount, Error.Wrap(err)
			}
		}
		return archivedCount, nil
	case dbutil.Postgres:
		err := db.db.DB.QueryRow(ctx, `
			WITH rollups_to_move AS (
				DELETE FROM bucket_bandwidth_rollups
				WHERE interval_start <= $1
				RETURNING *
			), moved_rollups AS (
				INSERT INTO bucket_bandwidth_rollup_archives(bucket_name, project_id, interval_start, interval_seconds, action, inline, allocated, settled)
				SELECT bucket_name, project_id, interval_start, interval_seconds, action, inline, allocated, settled FROM rollups_to_move
				RETURNING *
			)
			SELECT count(*) FROM moved_rollups
		`, before).Scan(&archivedCount)
		return archivedCount, Error.Wrap(err)
	default:
		return 0, nil
	}
}

func (db *ProjectAccounting) archiveRollupsBeforeByAction(ctx context.Context, action int32, before time.Time, batchSize int) (archivedCount int, err error) {
	defer mon.Task()(&ctx)(&err)

	for {
		var rowCount int
		err := db.db.QueryRow(ctx, `
			WITH rollups_to_move AS (
				DELETE FROM bucket_bandwidth_rollups
				WHERE action = $1 AND interval_start <= $2
				LIMIT $3 RETURNING *
			), moved_rollups AS (
				INSERT INTO bucket_bandwidth_rollup_archives(bucket_name, project_id, interval_start, interval_seconds, action, inline, allocated, settled)
				SELECT bucket_name, project_id, interval_start, interval_seconds, action, inline, allocated, settled FROM rollups_to_move
				RETURNING *
			)
			SELECT count(*) FROM moved_rollups
		`, int(action), before, batchSize).Scan(&rowCount)
		if err != nil {
			return archivedCount, Error.Wrap(err)
		}
		archivedCount += rowCount

		if rowCount < batchSize {
			return archivedCount, nil
		}
	}
}

// getBucketsSinceAndBefore lists distinct bucket names for a project within a specific timeframe.
func (db *ProjectAccounting) getBucketsSinceAndBefore(ctx context.Context, projectID uuid.UUID, since, before time.Time) (_ []string, err error) {
	defer mon.Task()(&ctx)(&err)
	bucketsQuery := db.db.Rebind(`SELECT DISTINCT bucket_name
		FROM bucket_storage_tallies
		WHERE project_id = ?
		AND interval_start >= ?
		AND interval_start <= ?`)
	bucketRows, err := db.db.QueryContext(ctx, bucketsQuery, projectID[:], since, before)
	if err != nil {
		return nil, err
	}
	defer func() { err = errs.Combine(err, bucketRows.Close()) }()

	var buckets []string
	for bucketRows.Next() {
		var bucket string
		err = bucketRows.Scan(&bucket)
		if err != nil {
			return nil, err
		}

		buckets = append(buckets, bucket)
	}

	return buckets, bucketRows.Err()
}

// timeTruncateDown truncates down to the hour before to be in sync with orders endpoint.
func timeTruncateDown(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
}

// GetProjectLimits returns current project limit for both storage and bandwidth.
func (db *ProjectAccounting) GetProjectLimits(ctx context.Context, projectID uuid.UUID) (_ accounting.ProjectLimits, err error) {
	defer mon.Task()(&ctx)(&err)

	row, err := db.db.Get_Project_BandwidthLimit_Project_UsageLimit_By_Id(ctx,
		dbx.Project_Id(projectID[:]),
	)
	if err != nil {
		return accounting.ProjectLimits{}, err
	}

	return accounting.ProjectLimits{
		Usage:     row.UsageLimit,
		Bandwidth: row.BandwidthLimit,
	}, nil
}

// GetRollupsSince retrieves all archived rollup records since a given time.
func (db *ProjectAccounting) GetRollupsSince(ctx context.Context, since time.Time) (bwRollups []orders.BucketBandwidthRollup, err error) {
	defer mon.Task()(&ctx)(&err)

	pageLimit := db.db.opts.ReadRollupBatchSize
	if pageLimit <= 0 {
		pageLimit = 10000
	}

	var cursor *dbx.Paged_BucketBandwidthRollup_By_IntervalStart_GreaterOrEqual_Continuation
	for {
		dbxRollups, next, err := db.db.Paged_BucketBandwidthRollup_By_IntervalStart_GreaterOrEqual(ctx,
			dbx.BucketBandwidthRollup_IntervalStart(since),
			pageLimit, cursor)
		if err != nil {
			return nil, Error.Wrap(err)
		}
		cursor = next
		for _, dbxRollup := range dbxRollups {
			projectID, err := uuid.FromBytes(dbxRollup.ProjectId)
			if err != nil {
				return nil, err
			}
			bwRollups = append(bwRollups, orders.BucketBandwidthRollup{
				ProjectID:  projectID,
				BucketName: string(dbxRollup.BucketName),
				Action:     pb.PieceAction(dbxRollup.Action),
				Inline:     int64(dbxRollup.Inline),
				Allocated:  int64(dbxRollup.Allocated),
				Settled:    int64(dbxRollup.Settled),
			})
		}
		if cursor == nil {
			return bwRollups, nil
		}
	}
}

// GetArchivedRollupsSince retrieves all archived rollup records since a given time.
func (db *ProjectAccounting) GetArchivedRollupsSince(ctx context.Context, since time.Time) (bwRollups []orders.BucketBandwidthRollup, err error) {
	defer mon.Task()(&ctx)(&err)

	pageLimit := db.db.opts.ReadRollupBatchSize
	if pageLimit <= 0 {
		pageLimit = 10000
	}

	var cursor *dbx.Paged_BucketBandwidthRollupArchive_By_IntervalStart_GreaterOrEqual_Continuation
	for {
		dbxRollups, next, err := db.db.Paged_BucketBandwidthRollupArchive_By_IntervalStart_GreaterOrEqual(ctx,
			dbx.BucketBandwidthRollupArchive_IntervalStart(since),
			pageLimit, cursor)
		if err != nil {
			return nil, Error.Wrap(err)
		}
		cursor = next
		for _, dbxRollup := range dbxRollups {
			projectID, err := uuid.FromBytes(dbxRollup.ProjectId)
			if err != nil {
				return nil, err
			}
			bwRollups = append(bwRollups, orders.BucketBandwidthRollup{
				ProjectID:  projectID,
				BucketName: string(dbxRollup.BucketName),
				Action:     pb.PieceAction(dbxRollup.Action),
				Inline:     int64(dbxRollup.Inline),
				Allocated:  int64(dbxRollup.Allocated),
				Settled:    int64(dbxRollup.Settled),
			})
		}
		if cursor == nil {
			return bwRollups, nil
		}
	}
}
