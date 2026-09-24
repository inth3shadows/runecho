"""Module docstring with a brace { and a def x( that must not count."""
import os


def compute(*a): return a
def outer(*a): return a
def call(*a): return a

msg = f"{compute(
    a, b
)}"
MAX_RETRIES = 5

cfg = {
    "a": 1,
    "b": [
        2, 3,
    ],
}; MAX_TIMEOUT = 30

def f(a,
      b=[
          1,
      ],
      c={"k": (1, 2)},
      *args, **kw):
    return call({
        "x": a,
    }, b)

def g(x: int = (1 +
                2)) -> dict:
    s = '''triple
    { not a dict
    def fake(
    '''
    return {"s": s}

) stray close
result = outer(
    inner[
        1,
    ],
    f"{x!r:>{width}}",
)

class K:
    def m(self, y=lambda z: (z,
                              z)):
        pass

def h(a,
      b)): pass
AFTER_H = 1

def fmt(a,
        b=f"{a:(^10}",
        c=1):
    pass
AFTER_FMT = 1

}{
MAX_VALUE = 1
