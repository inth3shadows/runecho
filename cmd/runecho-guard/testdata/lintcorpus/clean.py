"""Nothing here for F821 or F811 to say — the harness's zero-finding control."""

import os


def build_path(name):
    return os.path.join("/tmp", name)


def main():
    print(build_path("report.txt"))
