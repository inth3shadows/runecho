"""Oracle abstains: pyflakes/F821 stops reporting undefined names under a
star import (it answers F403/F405 instead), so ruff is silent on
`mystery_helper` while the guard's index question is unaffected.
"""

from os.path import *


def build(name):
    return join("/tmp", name, mystery_helper())
