#!/usr/bin/env python
# -*- coding: utf-8 -*-

import time
import random
import datetime
from string import ascii_letters


def generate_level():
    if random.random() < 0.8:
        return 'INFO'

    return 'ERROR'


def generate_time():
    dt = (datetime.datetime.now() + datetime.timedelta(hours=random.randint(-720, 720)))
    return dt.strftime('%Y-%m-%d %H:%M:%S') + "." + dt.strftime("%f")[:3]


def generate_message():
    return ''.join([random.choice(ascii_letters + '\n') for _ in range(100)])


def get_msg():
    return str(random.random())


def produce_log():
    l = generate_level()
    t = generate_time()
    m = get_msg()

    if random.random() < 0.5:
        return m

    return f"{t} | app | {l} | thread | class | 64: {m}"


def main():
    t = time.time()
    cnt = 0
    while 1:
        cnt += 1
        print(produce_log())
        time.sleep(0.1)
        if time.time() - t > 10:
            t = time.time()
            print(produce_log() + f"speed: {cnt/10}/s")
            cnt = 0


if __name__ == '__main__':
    main()
