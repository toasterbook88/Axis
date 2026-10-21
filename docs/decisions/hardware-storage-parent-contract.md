# Hardware storage parents and profiler containment

Status: accepted target contract; not implemented by the parser in this
test-first change. The parser at `bbfbb4d` violates the regression below.
This document does not describe shipped topology behavior.

## Two different relationships

For storage readings in `axis facts --hardware`:

| Reading | Meaning | Required evidence |
| --- | --- | --- |
| `parent` | Directly identified backing/block component represented by another device ID in the same Storage section | An explicit backing-device identifier or block relationship, such as the Linux `lsblk` relationship; JSON containment alone is insufficient |
| `profiler parent` | The containing record in the `system_profiler` JSON observation | The parser's containing record, with an existing, non-self-referencing local report ID |

`parent` is not a promise that the referenced component is a physical disk:
block-device layers may be virtual. It must never be inferred from a matching
model name, media description, capacity, mount path, or BSD name prefix.
This change targets macOS Storage; it does not redefine other sections' links.

When a profiler volume lacks an identifiable backing component, emit:

```json
{
  "name": "parent",
  "source": "system_profiler SPStorageDataType",
  "status": "unavailable",
  "reason": "backing device not identified by system_profiler"
}
```

The value must be absent/null. This does not mean the volume has no backing
device; it means this observation cannot identify it. On macOS, this parser
treats a nonempty `physical_drive.bsd_name` as explicit backing identity; a
missing, blank, or self-referencing value remains unavailable. Descriptive
`device_name` and `media_name` values never establish the link. The recorded
capture has no `physical_drive.bsd_name`.

## Drive descriptions are not device identity

The `physical_drive` map is a provider description nested under a volume. Give
its report record a meaningful name from nonempty `device_name`, then
`media_name`, otherwise `Unidentified backing device`. Preserve its containing
volume ID as an observed `profiler parent` reading, not as `parent`.

Do not merge provider descriptions just because their names match. Repeated
model descriptions do not establish a shared physical device. Conversely, a
separate report ID for each description does not establish distinct hardware.
A `Disk Image` description remains a virtual backing description; the provider's
field name must not turn it into a physical-disk claim.

The explicit `physical_drive.bsd_name` case is synthetic regression input: it
defines parser behavior if an observation supplies that identity; it is not a
claim that the recorded machine supplied one.

All emitted parent IDs must exist in the same section and must not reference
the record itself. Do not reverse an unproven edge and call it a repair.

## Permanent fixture and fail-first regression

`internal/facts/testdata/hardware-review/mac-profiler-nesting.json` retains all
map/list structure and the parser-selected scalar fields from a recorded
Apple Silicon profiler observation. The four `SPStorageDataType` entries have
these directly observed containment relationships:

| Containing volume BSD name | Nested `physical_drive.device_name` |
| --- | --- |
| `disk3s1` | `APPLE SSD AP0256Q` |
| `disk3s3` | `APPLE SSD AP0256Q` |
| `disk5s1` | `Disk Image` |
| `disk3s3s1` | `APPLE SSD AP0256Q` |

The old parser emits four generic `SPStorageDataType` records and labels those
volume IDs as their `parent`. The fixture regression locks the four input
records and volume associations; it must not permanently require that incorrect
parser output. The contract regression ties each parsed name and retained
provider field back to its expected containing volume, requires
`profiler parent` containment, and checks unavailable volume `parent` readings.
A separate nested Sensors case requires the existing non-storage `parent`
label, so the storage vocabulary stays scoped to Storage.

The earlier reduced `mac-profiler.json` omits all four nested maps, so it cannot
cover this regression. Keep the fuller capture alongside it. These are recorded
observations, not current machine inventory or native execution of a new parser.

Run the characterization and the currently failing contract separately:

```bash
./hack/hermetic-go-test.sh ./internal/facts -run '^TestMacStorageNestingFixture$' -count=1 -v
./hack/hermetic-go-test.sh ./internal/facts -run '^TestMacStorageParentContract$' -count=1 -v
```

The next implementation change must make the second command pass without
weakening the first. Before opening a reviewable PR, update this implementation
status, execute native Apple Silicon and Intel macOS verification, and run the
repository's required gates. Merge and installation remain separate actions.
