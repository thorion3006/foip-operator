# PRD: Failover Safety and Pluggable Health Verification

- **Document ID:** PRD-FOIP-SAFETY-001
- **Status:** Proposed
- **Target:** Post-v0.3.0 production-safety release series
- **Repository:** `thorion3006/foip-operator`
- **Last updated:** 2026-07-14

## 1. Executive summary

`foip-operator` v0.3.0 provides a two-phase make-before-break handoff:

1. select a desired node;
2. prepare the failover `/32` on that node;
3