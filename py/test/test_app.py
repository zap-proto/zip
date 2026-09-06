# SPDX-License-Identifier: BSD-3-Clause-Eco
"""What a declaration may be, refused where it is written.

Every refusal here could have been a shrug instead — a void direction, a field
that becomes `{}`, a second op quietly taking the first's name. Each is caught at
the decorator because the author is standing there; the alternative is an SDK
method nobody notices is wrong until a peer reads it.
"""

import unittest
from dataclasses import dataclass
from typing import Optional

import zip


@dataclass
class In:
    a: str


@dataclass
class Out:
    b: zip.u32


class Declaring(unittest.TestCase):
    def setUp(self):
        self.app = zip.App("svc")

    def test_the_annotations_are_the_declaration_and_the_docstring_is_the_prose(self):
        @self.app.post("/v1/things")
        def make(thing: In) -> Out:
            """Make one thing."""

        op, = self.app.ops()
        self.assertEqual((op.method, op.path, op.id), ("POST", "/v1/things", "post_things"))
        self.assertEqual((op.request, op.reply, op.doc), (In, Out, "Make one thing."))

    def test_a_handler_is_still_a_function(self):
        @self.app.post("/v1/things")
        def make(thing: In) -> Out:
            """Make one thing."""
            return Out(b=len(thing.a))

        self.assertEqual(make(In(a="abc")), Out(b=3))

    def test_a_direction_may_carry_nothing(self):
        @self.app.get("/v1/ready")
        def ready() -> None:
            """Say nothing, twice."""

        op, = self.app.ops()
        self.assertEqual((op.request, op.reply), (None, None))

    def refuses(self, fn, *, on="/v1/x"):
        with self.assertRaises(zip.Refused) as e:
            self.app.post(on)(fn)
        return str(e.exception)

    def test_a_handler_that_does_not_say_what_it_answers(self):
        def mute(x: In):
            """No return annotation at all."""

        self.assertIn("does not say what it answers", self.refuses(mute))

    def test_a_handler_that_does_not_say_what_it_takes(self):
        def mute(x) -> Out:
            """No argument annotation at all."""

        self.assertIn("does not say what x is", self.refuses(mute))

    def test_a_handler_taking_more_than_the_request(self):
        def two(x: In, y: In) -> Out:
            """A method carries one declaration per direction."""

        self.assertIn("one declaration per direction", self.refuses(two))

    def test_a_direction_that_is_not_a_declaration(self):
        def scalar(x: In) -> int:
            """An int is not a set of fields."""

        self.assertIn("declares no fields", self.refuses(scalar))

    def test_an_optional_reply_is_not_a_reply(self):
        def maybe(x: In) -> Optional[Out]:
            """A method answers with one declaration or with nothing, never both."""

        self.assertIn("declares no fields", self.refuses(maybe))

    def test_one_address_declared_twice(self):
        @self.app.post("/v1/things")
        def make(thing: In) -> Out:
            """The first."""

        def again(thing: In) -> Out:
            """The second, under the same id."""

        self.assertIn("declared twice", self.refuses(again, on="/v1/things"))


if __name__ == "__main__":
    unittest.main()
