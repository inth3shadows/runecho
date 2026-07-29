"""Differential corpus for the #243 call-shape extractor.

Every construct here disagreed with CPython at some point during step 1, or is a
neighbour of one that did. TestCallShapeDifferential parses this file with
CPython's `ast` module and requires the Go extractor to agree on every shape it
emits — so a regression in the masking or splitting logic fails loudly rather
than silently widening the guard's false-positive surface.

It is deliberately real-looking code rather than a list of snippets: the bugs
found in step 1 all came from ordinary formatting (aligned f-string headers,
comments inside argument lists, string-literal format specs), not from anything
exotic.
"""


def _fmt(value, spec):
    return format(value, spec)


def _results_frame(game_ids, seasons, weeks, confidences, corrects):
    return list(zip(game_ids, seasons, weeks, confidences, corrects))


def render(ctx, name=None, level=0):
    return (ctx, name, level)


def build(rows=(), label="", strict=False):
    return (rows, label, strict)


def dispatch(first, *rest, mode="x", **extra):
    return (first, rest, mode, extra)


def section_header(gap_prop, gap_power):
    # A string-literal argument masks to whitespace, which once made this read as
    # a single-argument call.
    print(f"| Proportional | {_fmt(gap_prop, '+.4f')} |")
    print(f"| Power        | {_fmt(gap_power, '+.4f')} |")
    # A nested literal inside an interpolation is data: `acc(curr)` is not a call.
    print(f"{'Season':>7} | {'acc(curr)':>10} | {'ll(w*)':>8}")
    # ...but a genuine call in the same interpolation must still be seen.
    print(f"{_fmt(gap_prop, '.2f')} and {_fmt(gap_power, '.2f')}")


def multiline_calls():
    frame = _results_frame(
        game_ids=["g1", "g2"],
        seasons=[2024] * 2,
        weeks=[1] * 2,
        confidences=[0.9, 0.8],
        corrects=[True, None],  # a comment interleaved with the arguments
    )
    plain = _results_frame(
        game_ids=["g1"],
        seasons=[2024],
        weeks=[1],
        confidences=[0.5],
        corrects=[True],
    )
    spread = build(
        # a comment on its own line, before the value
        rows=[1, 2, 3],
        label="x",
    )
    return frame, plain, spread


def operators_are_not_keywords(a, b, xs, d):
    render(a == b)
    render(a != b)
    render(a <= b)
    render(a >= b)
    render(n := len(xs))
    render(d["k"] == 1)
    render(spec="a=b")
    return None


def unpackings(args, kwargs):
    dispatch("first", *args, mode="y", **kwargs)
    dispatch("first", "second", mode="z")
    return None


def lambdas(xs):
    # A top-level lambda's parameter comma is indistinguishable from an argument
    # comma; a bracket-nested one is not.
    build(rows=sorted(xs, key=lambda r: r[0]), label="nested")
    render(sorted(xs, key=lambda a, b: a))
    return None


def trailing_commas(ctx):
    render(ctx,)
    render(ctx, name="x",)
    build()
    return None


def strings_cannot_skew(ctx):
    render(ctx, name="oops) level=9")
    render(ctx, name='mixed "quotes" inside')
    render(ctx, name="escaped \" quote")
    return None


def nested_and_qualified(client, ctx):
    render(build(rows=[1], label="inner"), name="outer")
    client.render(ctx, name="qualified — not extracted")
    return None


def comprehensions(a, b, items):
    # An unparenthesized genexp puts a comma at depth zero, exactly like a lambda's
    # parameters: one argument, two segments. CPython's statistics.py and
    # dataclasses.py both write it this way.
    _fmt(w / x for w, x in zip(a, b))
    _results_frame(k for k, v in items())
    build(rows=[(k, v) for k, v in items()])
    render(dict((k, v) for k, v in items()))
    return None


def continued_method_chain(table, raw):
    # The `.` is left on the previous line, so the callee below looks bare.
    return table.replace("a", "b"). \
        replace("c", "d"), raw.decode("utf-8"). \
        strip()


def pattern_matching(value):
    # Structural pattern matching is not a call: CPython models `case build(...)` as
    # MatchClass, so reading it as a call fabricates an arity claim against `build`.
    match value:
        case build():
            return "build"
        case render(ctx=0, name=0):
            return "render"
        case _:
            return None
