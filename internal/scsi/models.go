package scsi

import (
	"encoding/json"

	"github.com/n-popescu/grid0plus/internal/db"
)

// archiveView is the JSON shape of a save data archive.
//
// Field names and the split between "listed" and "requested individually"
// (component_files only appears on the latter) are confirmed from
// https://github.com/kinnay/NintendoClients/wiki/OLSC-Servers#save-data-archive.
type archiveView struct {
	ID            string `json:"id"`
	NSAID         string `json:"nsa_id"`
	ApplicationID string `json:"application_id"`
	DeviceID      string `json:"device_id"`
	DeviceSerial  string `json:"device_serial"`
	DataSize      int64  `json:"data_size"`
	Desc          string `json:"desc"`
	// json.Number, not int64: a real value observed on real hardware
	// 2026-09-24 (12237957226414712540) exceeds int64's own maximum. See
	// db.SaveDataArchive.SeriesID's own comment. json.Number marshals as a
	// bare JSON number, matching a real console's own response shape,
	// while holding the exact digit string internally rather than losing
	// precision to float64 or overflowing a fixed-width integer.
	SeriesID                  json.Number     `json:"series_id"`
	Datatype                  int             `json:"datatype"`
	Status                    string          `json:"status"`
	AutoBackup                bool            `json:"auto_backup"`
	NumOfPartitions           int             `json:"num_of_partitions"`
	LaunchRequiredVersion     int             `json:"launch_required_version"`
	PreviousSaveDataArchiveID string          `json:"previous_save_data_archive_id,omitempty"`
	EncodedDigest             string          `json:"encoded_digest"`
	SavedAtUnixtime           int64           `json:"saved_at_as_unixtime"`
	FinishedAtUnixtime        int64           `json:"finished_at_as_unixtime,omitempty"`
	ComponentFiles            []componentView `json:"component_files,omitempty"`
}

// componentView is the JSON shape of one component file object.
//
// Reference: same wiki page, "A component file object has the following
// fields".
type componentView struct {
	ID                   string `json:"id"`
	Index                int    `json:"index"`
	Datatype             string `json:"datatype"`
	Status               string `json:"status"`
	ArchiveSize          int64  `json:"archive_size"`
	EncodedArchiveDigest string `json:"encoded_archive_digest"`
}

func toArchiveView(a db.SaveDataArchive, components []db.ComponentFile) archiveView {
	v := archiveView{
		ID: a.ID, NSAID: a.NSAID, ApplicationID: a.ApplicationID,
		DeviceID: a.DeviceID, DeviceSerial: a.DeviceSerial,
		DataSize: a.DataSize, Desc: a.Description, SeriesID: json.Number(a.SeriesID),
		Datatype: a.Datatype, Status: a.Status, AutoBackup: a.AutoBackup,
		NumOfPartitions: a.NumOfPartitions, LaunchRequiredVersion: a.LaunchRequiredVersion,
		PreviousSaveDataArchiveID: a.PreviousSaveDataArchiveID,
		EncodedDigest:             a.EncodedDigest,
		SavedAtUnixtime:           a.SavedAt.Unix(),
	}
	if a.FinishedAt != nil {
		v.FinishedAtUnixtime = a.FinishedAt.Unix()
	}
	if components != nil {
		v.ComponentFiles = make([]componentView, len(components))
		for i, c := range components {
			v.ComponentFiles[i] = toComponentView(c)
		}
	}
	return v
}

func toComponentView(c db.ComponentFile) componentView {
	return componentView{
		ID: c.ID, Index: c.Index, Datatype: c.Datatype, Status: c.Status,
		ArchiveSize: c.ArchiveSize, EncodedArchiveDigest: c.EncodedArchiveDigest,
	}
}
