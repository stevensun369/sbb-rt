package main

import (
	"database/sql"
	"fmt"
)

type GTFSRepository interface {
	ResolveTrip(string) (tripMeta, error)
	Connection(tripMeta, tripMeta) (connection, error)
	PlatformForStop(string) (string, error)
}

type tripMeta struct {
	TripID      string
	RouteID     string
	Origin      stopSchedule
	Destination stopSchedule
}

type stopSchedule struct {
	StopID   string
	Name     string
	Platform string
	Time     string
}

type SQLiteGTFS struct{ db *sql.DB }

func NewSQLiteGTFS(db *sql.DB) *SQLiteGTFS { return &SQLiteGTFS{db: db} }

func (g *SQLiteGTFS) ResolveTrip(tripID string) (tripMeta, error) {
	var meta tripMeta
	var routeID string
	if err := g.db.QueryRow(`SELECT trip_id, route_id FROM trips WHERE trip_id = ?`, tripID).Scan(&meta.TripID, &routeID); err != nil {
		return meta, fmt.Errorf("resolve trip %q: %w", tripID, err)
	}
	meta.RouteID = routeID
	rows, err := g.db.Query(`
		SELECT st.stop_id, COALESCE(s.stop_name,''), COALESCE(s.platform_code,''), st.arrival_time
		FROM stop_times st LEFT JOIN stops s ON s.stop_id = st.stop_id
		WHERE st.trip_id = ? ORDER BY st.stop_sequence`, tripID)
	if err != nil {
		return meta, err
	}
	defer rows.Close()
	var stops []stopSchedule
	for rows.Next() {
		var stop stopSchedule
		if err := rows.Scan(&stop.StopID, &stop.Name, &stop.Platform, &stop.Time); err != nil {
			return meta, err
		}
		stops = append(stops, stop)
	}
	if err := rows.Err(); err != nil {
		return meta, err
	}
	if len(stops) == 0 {
		return meta, fmt.Errorf("trip %q has no stop times", tripID)
	}
	meta.Origin, meta.Destination = stops[0], stops[len(stops)-1]
	return meta, nil
}

func (g *SQLiteGTFS) Connection(from, to tripMeta) (connection, error) {
	var c connection
	err := g.db.QueryRow(`
		SELECT from_stop_id, to_stop_id, COALESCE(min_transfer_time,'0')
		FROM transfers
		WHERE from_trip_id = ? AND to_trip_id = ?
		ORDER BY CAST(COALESCE(min_transfer_time,'0') AS INTEGER)
		LIMIT 1`, from.TripID, to.TripID).Scan(&c.Station.StopID, &c.ToPlatform, &c.MinimumTransferSeconds)
	if err != nil {
		// Static feeds commonly describe station-level transfers by route/stop.
		if err2 := g.db.QueryRow(`
			SELECT from_stop_id, to_stop_id, COALESCE(min_transfer_time,'0')
			FROM transfers WHERE from_route_id = ? AND to_route_id = ?
			ORDER BY CAST(COALESCE(min_transfer_time,'0') AS INTEGER) LIMIT 1`,
			from.RouteID, to.RouteID).Scan(&c.Station.StopID, &c.ToPlatform, &c.MinimumTransferSeconds); err2 != nil {
			return c, fmt.Errorf("resolve connection %s -> %s: %w", from.TripID, to.TripID, err2)
		}
	}
	var name, fromPlatform, toPlatform string
	_ = g.db.QueryRow(`SELECT COALESCE(stop_name,''), COALESCE(platform_code,'') FROM stops WHERE stop_id = ?`, c.Station.StopID).Scan(&name, &fromPlatform)
	_ = g.db.QueryRow(`SELECT COALESCE(platform_code,'') FROM stops WHERE stop_id = ?`, c.ToPlatform).Scan(&toPlatform)
	c.Station.Name, c.FromPlatform, c.ToPlatform = name, fromPlatform, toPlatform
	c.FromLeg, c.ToLeg = 0, 0
	return c, nil
}

func (g *SQLiteGTFS) PlatformForStop(stopID string) (string, error) {
	var platform string
	err := g.db.QueryRow(`SELECT COALESCE(platform_code,'') FROM stops WHERE stop_id = ?`, stopID).Scan(&platform)
	return platform, err
}
