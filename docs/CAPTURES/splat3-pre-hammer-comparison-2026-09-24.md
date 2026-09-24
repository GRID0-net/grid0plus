# Splatoon 3: response differences before the Hammer read

The 16:18 UTC SwitchNet hardware attempt used an account-scoped, populated SaveRecord substituted from the preserved working Nextendo capture. The console visibly loaded that save but still showed the hall communication error. NPLN logged the same first late document read, Hammer/UserAttribute/Data, followed by NotFound; it never logged reads for the substituted GameRecord Coop or Versus attribute documents. The diagnostic override and all temporary VM protobuf files were removed after the test.

The working capture takes a different path **before** Canola and Locker: following the second schedule batch, SelectFestSchedule and SelectVsParams, it reads GameRecord CoopUserAttribute/Data and VsUserAttribute/Data, then several other GameRecord documents and calls InitializeAttributes. It only then requests Canola/SelectDocuments, Locker/SelectDocuments, and RunQuery. SwitchNet's 16:18 attempt proceeded directly from the first schedule/fest responses to Canola and Locker, then RunQuery, another schedule refresh, and the absent Hammer document. This order is an observation, not proof that any one response controls the branch; the working and failing accounts have different histories.

| Response | Working Nextendo capture | SwitchNet 16:18 path | Status |
| --- | --- | --- | --- |
| CloudSave/GetSaveRecord | Populated, 87-key record | Same captured record served in the private test | Save contents alone did not advance the branch. |
| Schedule/SelectVsParams | Populated parameters, including 11 attributes | Own parameter resource with an empty attribute map | Confirmed content gap; required values or client fallback remain unknown. |
| FestService/SelectFestSchedule | One empty protobuf message (five framed bytes) | An expired, locally generated sentinel schedule | Confirmed response-shape gap. Nextendo source comments recount crashes with an empty schedule in other client states, yet this preserved working session received one and continued. Do not generalize either observation. |
| Schedule/SelectLeagueSchedules | Two schedules | Empty list | Content gap, but no operator-authored League event exists here. |
| Canola/Locker SelectDocuments | Published art/locker entries | Empty lists | Later content gap; the client already skipped the early GameRecord reads before reaching these. |
| Ugcstore/RunQuery for rewards | One read-time-only message | One read-time-only message | Matches the observed empty-collection shape. |
| Ugcstore/GetDocument for absent Hammer | Two-frame, zero-body NotFound with NError | Same status, header and detail shape | The working client continues through this error; changing Hammer to an invented document would be unsupported. |

Nextendo's `ugcstore_absent.go` documents failed alternate shapes for absent documents and retains the two-frame detailed NotFound. Its historical commentary is not a wire capture. The preserved working flow's trailers independently confirm NotFound for Hammer. No response patch is warranted on that path.

The strongest *untested* pre-branch candidates are the empty VsParams attribute map and the expired fest sentinel, not the borrowed save or Hammer error. Both need a controlled test because neither the working capture nor Nextendo's historical comments establishes what a fresh, uninitialized SwitchNet account requires. The next diagnostic should capture SwitchNet's actual plaintext gRPC response bodies and timestamps for the first two schedule batches, FestSchedule, and VsParams, alongside the client's first subsequent RPC. Compare those with the preserved working flow before altering one response at a time. Do not copy captured player data or Nextendo source into this repository.

A later read-only decode of both working captures found one more shared pre-branch mismatch: the four populated schedule/parameter responses set protobuf field 1 (`unk`) to true, while SwitchNet omitted it. See [npln-schedule-update-flag-2026-09-24.md](npln-schedule-update-flag-2026-09-24.md).
