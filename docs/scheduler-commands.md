# Scheduler commands and the web UI

Every change the web UI makes to the observatory's scheduler is a **command**. The stacker records it in its own database (`scheduler_commands`, the History page), then delivers it to the observatory-scheduler plugin. The plugin is the only writer to the Target Scheduler database.

## The envelope

```json
{ "id": "uuid", "kind": "project.edit", "payload": { }, "author": "web", "undo_of": "uuid or empty", "created_at": "RFC3339" }
```

The plugin answers with a result: `{ "id", "status", "message", "detail", "applies_at", "updated_at" }`.

| Status | Meaning |
|---|---|
| `queued` | Not delivered to the observatory yet. |
| `pending` | Delivered; it applies when the current exposure ends. |
| `applied` | Applied by the plugin. |
| `conflict` | A `before` value no longer matched the database. Nothing changed. |
| `rejected` / `failed` | Refused (for example, web editing is off) or errored. |
| `cancelled` | Withdrawn before it applied. |
| `saved` | App-only command, applied by the stacker itself. |

## Delivery

1. **Fast path:** `POST /os/v1/commands` on the plugin's API, over the tailnet.
2. **Durable path:** a row in `ts_command` in `schedulerdb`. SymmetricDS carries it home → observatory; the plugin writes `ts_command_result`, which flows back.

Both paths use the same id, and the plugin ignores an id it has already seen, so a command can travel both ways safely. Home only ever writes `ts_command`; the observatory only ever writes `ts_command_result`.

```sql
ts_command (id text primary key, kind text, payload text, author text, undo_of text, created_at timestamp, cancelled integer default 0)
ts_command_result (command_id text primary key, status text, message text, detail text, updated_at timestamp)
```

## Undo and cancel

- Each kind declares its inverse (`Spec.Inverse`). Undo sends the inverse as a new command with `undo_of` set; the original stays in History with `undone_by`.
- Field edits (`EditPayload`) carry `before` and `after` for each field. The inverse swaps them, and the plugin rejects any change whose `before` no longer matches (`conflict`).
- Undo of a command that is still `queued` or `pending` cancels it instead.
- A kind with no inverse returns `ErrNoInverse`, and History shows it as not undoable.

## Adding a command kind

**Stacker** (`internal/schedcmd`):
1. Add a `Kind` constant.
2. Implement `Spec`, or use `EditSpec` for a plain field edit (list the editable fields).
3. Add it to `Builtin()`.
4. For an app-only kind, set `Destination()` to `DestinationApp` and implement `AppApplier`.
5. Add a sample payload to `TestEveryKindHasAnInverseOrSaysNo`.

**Plugin** (`NINA.Plugin.TargetScheduler/Remote/Commands`):
1. Implement `ICommandHandler`, or add the fields to the entity's `EditHandler`.
2. Register it in `CommandRegistry.Builtin()`.
3. Add a unit test.

The kind string and the payload JSON must match exactly on both sides.

## Adding a page

1. Put the page in `web/src/pages/`. Each route in `web/src/router.ts` already points at a placeholder page.
2. Call the API through `web/src/api/client.ts`, and send changes with `submitCommand` or `editEntity` from `web/src/api/commands.ts`.
3. Show the result with `notifyCommand(record)` from `web/src/shell.ts`. It raises the toast with Undo and updates the top-bar indicator.
4. Use the shared classes in `web/src/styles.css` (`page`, `card`, `btn`, `badge`, `field`, `input`, `dialog`, `grid`).

Build with `cd web && npm ci && npm run build`, or `go generate ./internal/webui`. `npm run dev` proxies `/api` to `STACKER_URL` (default `http://localhost:8080`).
