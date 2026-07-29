# Task: Implement Phase 5 Item 1 - Interactive Terminal Session Recording & Replay Engine

Refer to `implementation_plan.md` and `PROJECT_KNOWLEDGE.md` before starting. Follow `agentrule.md` strictly (run builds and tests via Docker using `make verify` / `make test`).

## Key Tasks to Implement:

1. **Database Schema & Models (`backend/pkg/db/terminal_session_models.go`, `store.go`)**:
   - Create `TerminalSession` struct (`ID`, `AuditID`, `UserID`, `Username`, `ClusterID`, `Namespace`, `PodName`, `ContainerName`, `Shell`, `StartedAt`, `EndedAt`, `DurationSeconds`, `ByteCount`, `StoragePath`).
   - Add DB migration to create `terminal_sessions` table and CRUD methods in `store.go`.

2. **Stream Recorder Engine (`backend/pkg/terminal/recorder.go`, `pkg/bastion/exec.go`)**:
   - Create `AsciinemaRecorder` writing gzip-compressed Asciinema v2 JSON line frames (`[offset, "o"|"i", data]`).
   - Intercept WebSocket container exec stream in `pkg/bastion/exec.go` to duplex stdout/stderr/stdin to `AsciinemaRecorder`.
   - Compress and save `.cast.gz` session recordings to persistent storage directory.

3. **Backend API Endpoints (`backend/pkg/api/terminal_sessions.go`, `routes.go`)**:
   - `GET /api/v1/audit/terminal-sessions`: Query & list recorded sessions.
   - `GET /api/v1/audit/terminal-sessions/:id`: Fetch session details.
   - `GET /api/v1/audit/terminal-sessions/:id/stream`: Stream decompressed asciinema stream data for UI player.
   - `DELETE /api/v1/audit/terminal-sessions/:id`: Delete recording (Admin only).

4. **Frontend UI Replay Player (`frontend/src/components/terminal/TerminalSessionPlayer.tsx`)**:
   - Create `TerminalSessionPlayer.tsx` using XTerm.js read-only canvas with Signal Deck styling.
   - Implement playback controls: Play/Pause, timeline scrubber slider, speed options (0.5x, 1x, 2x, 4x, 8x), copy terminal output button.
   - Update `AuditLogViewer.tsx` to display a **Replay Terminal Session** action button on `exec` audit rows opening the player in `Sheet` drawer.

5. **Verification & Roadmap Update**:
   - Run `make verify` inside Docker containers to ensure backend tests and frontend builds pass cleanly.
   - Update `roadmap.md` to check off (`[x]`) Phase 5 Item 1: `- [x] Interactive Terminal Session Recording & Replay`.
