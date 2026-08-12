"""F821 on a symbol the additive check also flags — exercises suppression.

`missing_helper` is neither defined here nor present in any index, so ruff and
guard.Run answer the same question two ways and suppressAlreadyReported must
collapse them to one report.
"""


def run(value):
    return missing_helper(value)
