CC = gcc
CFLAGS = -Wall -O2 -pthread
TARGETS = stress_test tun_close

all: $(TARGETS)

stress_test: stress_test.c
	$(CC) $(CFLAGS) -o stress_test stress_test.c

tun_close: tun_close.c
	$(CC) $(CFLAGS) -o tun_close tun_close.c

clean:
	rm -f $(TARGETS)
