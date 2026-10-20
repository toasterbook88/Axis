# Hardware review fixtures

Captured without privilege escalation on 2026-10-19; only relevant fields retained.

- `smart-permission.json`: Linux smartctl 7.4, `smartctl --json --all /dev/sda`, uid 1000, exit 2, permission denied. No health fields were present in the real output. Tests add `smart_status.passed=false` and `temperature.current=0` to simulate a failed probe with stub fields; this is not an observed disk-health failure.
- `mac-profiler.json`: real Apple Silicon macOS `system_profiler -json -detailLevel mini SPNetworkDataType SPStorageDataType`. Network/storage identity and type fields retained; addresses, UUIDs, serials, and other fields omitted. The dump has no empty IDs. Tests explicitly replace identity values with empty strings to exercise high-fidelity edge variants.
- `pci-nvidia.json`: one real NVIDIA PCI GPU from Linux `/sys/bus/pci/devices/*`; AMD telemetry files returned ENOENT (errno 2). Tests substitute Intel/AMD vendor IDs as labeled synthetic controls. No Intel GPU was present on the capture host.
