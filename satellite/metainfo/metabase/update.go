// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

package metabase

import (
	"context"
	"database/sql"
	"errors"

	"storj.io/common/storj"
	"storj.io/common/uuid"
	"storj.io/storj/storage"
)

// UpdateSegmentPieces contains arguments necessary for updating segment pieces.
type UpdateSegmentPieces struct {
	StreamID uuid.UUID
	Position SegmentPosition

	OldPieces Pieces
	NewPieces Pieces
}

// UpdateSegmentPieces updates pieces for specified segment. If provided old pieces
// won't match current database state update will fail.
func (db *DB) UpdateSegmentPieces(ctx context.Context, opts UpdateSegmentPieces) (err error) {
	defer mon.Task()(&ctx)(&err)

	switch {
	case opts.StreamID.IsZero():
		return ErrInvalidRequest.New("StreamID missing")
	case len(opts.NewPieces) == 0:
		return ErrInvalidRequest.New("NewPieces missing")
	}

	var pieces Pieces
	err = db.db.QueryRow(ctx, `
		UPDATE segments SET
			remote_pieces = CASE
				WHEN remote_pieces = $3 THEN $4
				ELSE remote_pieces
			END
		WHERE
			stream_id     = $1 AND
			position      = $2
		RETURNING remote_pieces
		`, opts.StreamID, opts.Position, opts.OldPieces, opts.NewPieces).Scan(&pieces)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// TODO should we have something like ErrSegmentNotFound
			return storj.ErrObjectNotFound.New("segment not found")
		}
		return Error.New("unable to update segment pieces: %w", err)
	}

	if !opts.NewPieces.Equal(pieces) {
		return storage.ErrValueChanged.New("segment remote_pieces field was changed")
	}

	return nil
}