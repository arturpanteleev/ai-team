# Global Event Feed

## Why

Operators need one place to review lifecycle events across tasks, isolate
warnings, and open the task that produced an event.

## What Changes

- Add a global event feed page backed by the persisted event API and live
  WebSocket stream.
- Add an all-events and warnings-only filter, newest-first cursor pagination,
  and task links.
- Add `order=desc` and `before` to the event API so the feed opens on the
  newest events; ascending `after` pagination stays the default.
- Resolve stable run IDs from events to the existing task detail page.

## Impact

- A new `Лента` navigation item and `/events` page.
- A stable `/runs/:runID` UI link that resolves to the existing
  `/pipelines/:id` task detail route.
- No new backend event storage format; the feed reuses the existing global
  cursor and warning contract. Existing `after` clients are unchanged.
