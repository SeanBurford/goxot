CC = gcc
CFLAGS = -Wall -O2 -pthread
TARGET = stress_test

all: $(TARGET)

$(TARGET): stress_test.c
	$(CC) $(CFLAGS) -o $(TARGET) stress_test.c

clean:
	rm -f $(TARGET)
