# gmk iteration examples

Two combinators shipped in Stage 3c.2:

- **`${map:fn(items=L, pinned...)}`** — call `fn(item=X, pinned...)` for each `X` in `L`. Returns a List of the function results.
- **`${filter:fn(items=L, pinned...)}`** — call `fn(item=X, pinned...)` for each `X` in `L`. The predicate `fn` must return a Bool. Returns a List containing only those `X` where the predicate returned true.

Both share the same arg shape — `items=L` is the iterable, everything else is "pinned" and passed through unchanged on every iteration. The iteration variable always binds to the named arg `item` (fixed in this initial cut; configurable `as=name` is future work).

| # | Directory       | Teaches                                              |
|---|-----------------|------------------------------------------------------|
| 1 | `01-map-filter/` | Basic map + filter, composition, `len()` on the result |

## How functions consume iteration args

`item` arrives as part of `$GMK_ARGS` (the JSON file the runner writes for each callable invocation). Functions read it the same way they'd read any other arg:

```python
import json, os
args = json.load(open(os.environ["GMK_ARGS"]))
item = args["item"]   # could be string, number, map, list — whatever the iterable holds
```

For bash, `jq` is the natural reader; `printf '%s' ... > "$GMK_RESULT"` won't work because `$GMK_RESULT` expects JSON — write with `json.dump(...)` from Python or `jq -n --arg x "$v" '$x' > "$GMK_RESULT"` from bash.

## What's not in this initial cut

- **`pmap:`** — parallel map. Needs the parallel-fanout primitive (next chunk).
- **Configurable iteration variable** (`as=svc`). Defer until first user needs it.
- **`reduce:`** / fold. Defer until first user needs it.
- **Iteration over Maps** (`items=mapvar` for `(key, value)` pairs). Defer; if needed, the natural shape is `${map_pairs:fn(items=M)}` calling `fn(key=K, value=V)`.
