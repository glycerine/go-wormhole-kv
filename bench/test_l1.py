"""Checks that the counter report rejects unusable measurements."""
import unittest
from l1 import counters


class CounterTests(unittest.TestCase):
    fixture = "\n".join([
        "100;;cycles:u;900;100.00;;",
        "200;;instructions:u;900;100.00;2;insn per cycle",
        "300;;L1-dcache-loads;900;100.00;;",
        "10;;L1-dcache-load-misses;900;100.00;;",
    ])

    def test_valid_group(self):
        self.assertEqual(counters(self.fixture), {
            "cycles": 100, "instructions": 200,
            "L1-dcache-loads": 300, "L1-dcache-load-misses": 10,
        })

    def test_reject_bad_counters(self):
        for broken in [self.fixture.replace("100.00", "50.00"),
                       self.fixture.replace("100;;cycles", "<not counted>;;cycles"),
                       self.fixture.replace("300;;L1", "0;;L1"),
                       self.fixture.splitlines()[0]]:
            with self.subTest(broken=broken), self.assertRaises(ValueError):
                counters(broken)


if __name__ == "__main__":
    unittest.main()
