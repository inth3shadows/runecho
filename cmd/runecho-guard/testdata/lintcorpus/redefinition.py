"""F811 only — the ruff-only partition in BOTH postures.

The additive check asks whether a symbol resolves, not whether it was already
bound; `fetch` resolves fine. Nothing but the linter has anything to say.
"""


def fetch():
    return 1


def fetch():
    return 2
