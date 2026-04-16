#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <pthread.h>
#include <sys/socket.h>
#include <sys/ioctl.h>
#include <linux/x25.h>
#include <time.h>
#include <errno.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <sys/time.h>
#include <limits.h>
#include <signal.h>

#define X25_ADDR_LEN 16

// Global statistics
typedef struct {
    atomic_long calls_made;
    atomic_long calls_received;
    atomic_long calls_failed;
    atomic_long bytes_sent;
    atomic_long bytes_received;
    atomic_long packets_sent;
    atomic_long packets_received;
    atomic_long data_mismatches;
    atomic_int max_pacsize_in;
    atomic_int max_pacsize_out;
    atomic_int max_winsize_in;
    atomic_int max_winsize_out;
    atomic_int min_pacsize_in;
    atomic_int min_pacsize_out;
    atomic_int min_winsize_in;
    atomic_int min_winsize_out;
} stats_t;

stats_t global_stats;

typedef struct {
    int num_threads;
    int buffer_size;
    char start_addr[X25_ADDR_LEN];
    char end_addr[X25_ADDR_LEN];
    char local_addr[X25_ADDR_LEN];
    int backoff_ms;
    int run_time_s;
    int max_calls;
    bool is_receiver;
    int window_size;
    int packet_size;
} config_t;

config_t cfg;

void update_max(atomic_int *ptr, int val) {
    int current = atomic_load(ptr);
    while (val > current && !atomic_compare_exchange_weak(ptr, &current, val));
}

void update_min(atomic_int *ptr, int val) {
    int current = atomic_load(ptr);
    while (val < current && !atomic_compare_exchange_weak(ptr, &current, val));
}

void show_cause_x25(int sock, const char *prefix) {
    struct x25_causediag causediag;
    memset(&causediag, 0, sizeof(causediag));
    if (ioctl(sock, SIOCX25GCAUSEDIAG, &causediag) >= 0) {
        if (causediag.cause != 0 || causediag.diagnostic != 0) {
            printf("%s Cause: 0x%02x, Diag: %d\n", prefix, causediag.cause, causediag.diagnostic);
        }
    }
}

void record_facilities(int sock) {
    struct x25_facilities facilities;
    memset(&facilities, 0, sizeof(facilities));
    if (ioctl(sock, SIOCX25GFACILITIES, &facilities) >= 0) {
        int pin = 1 << facilities.pacsize_in;
        int pout = 1 << facilities.pacsize_out;
        int win = facilities.winsize_in;
        int wout = facilities.winsize_out;

        update_max(&global_stats.max_pacsize_in, pin);
        update_max(&global_stats.max_pacsize_out, pout);
        update_max(&global_stats.max_winsize_in, win);
        update_max(&global_stats.max_winsize_out, wout);

        update_min(&global_stats.min_pacsize_in, pin);
        update_min(&global_stats.min_pacsize_out, pout);
        update_min(&global_stats.min_winsize_in, win);
        update_min(&global_stats.min_winsize_out, wout);
    }
}

struct timeval global_start_total;
void print_summary(double duration);

void sigint_handler(int sig) {
    struct timeval now;
    gettimeofday(&now, NULL);
    double duration = (now.tv_sec - global_start_total.tv_sec) + (now.tv_usec - global_start_total.tv_usec) / 1000000.0;
    printf("\nInterrupted by signal %d\n", sig);
    print_summary(duration);
    exit(0);
}

void fill_buffer(unsigned char *buf, int size, int thread_id, int call_id) {
    for (int i = 0; i < size; i++) {
        buf[i] = (unsigned char)((i ^ thread_id ^ call_id) & 0xFF);
    }
}

int get_pacsize_log(int size) {
    int log = 0;
    while (size > 1) {
        size >>= 1;
        log++;
    }
    return log;
}

void *sender_thread(void *arg) {
    int thread_id = (int)(long)arg;
    unsigned char *send_buf = malloc(cfg.buffer_size + 1);
    unsigned char *recv_buf = malloc(cfg.buffer_size + 1);
    
    long start_addr_val = atol(cfg.start_addr);
    long end_addr_val = atol(cfg.end_addr);
    long range = end_addr_val - start_addr_val + 1;
    if (range <= 0) range = 1;

    struct timeval start_tv;
    gettimeofday(&start_tv, NULL);

    int calls_count = 0;
    while (1) {
        struct timeval now_tv;
        gettimeofday(&now_tv, NULL);
        if (now_tv.tv_sec - start_tv.tv_sec >= cfg.run_time_s) break;
        if (cfg.max_calls > 0 && atomic_load(&global_stats.calls_made) >= cfg.max_calls) break;

        int sock = socket(AF_X25, SOCK_SEQPACKET, 0);
        if (sock < 0) {
            perror("socket");
            sleep(1);
            continue;
        }

        struct timeval timeout;
        timeout.tv_sec = 5;
        timeout.tv_usec = 0;
        if (setsockopt(sock, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout)) < 0) {
            perror("setsockopt(SO_RCVTIMEO)");
        }

        // Bind to local address if specified
        if (strlen(cfg.local_addr) > 0) {
            struct sockaddr_x25 laddr;
            memset(&laddr, 0, sizeof(laddr));
            laddr.sx25_family = AF_X25;
            strncpy(laddr.sx25_addr.x25_addr, cfg.local_addr, X25_ADDR_LEN - 1);
            if (bind(sock, (struct sockaddr *)&laddr, sizeof(laddr)) < 0) {
                perror("bind");
                close(sock);
                usleep(cfg.backoff_ms * 1000);
                continue;
            }
        }

        // Set facilities
        struct x25_facilities facilities;
        memset(&facilities, 0, sizeof(facilities));
        facilities.winsize_in = cfg.window_size;
        facilities.winsize_out = cfg.window_size;
        int pac_log = get_pacsize_log(cfg.packet_size);
        facilities.pacsize_in = pac_log;
        facilities.pacsize_out = pac_log;
        if (ioctl(sock, SIOCX25SFACILITIES, &facilities) < 0) {
            perror("ioctl(SIOCX25SFACILITIES)");
        }

        long target_val = start_addr_val + (rand() % range);
        char target_addr[X25_ADDR_LEN];
        snprintf(target_addr, X25_ADDR_LEN - 1, "%ld", target_val);

        struct sockaddr_x25 raddr;
        memset(&raddr, 0, sizeof(raddr));
        raddr.sx25_family = AF_X25;
        strncpy(raddr.sx25_addr.x25_addr, target_addr, X25_ADDR_LEN - 1);

        atomic_fetch_add(&global_stats.calls_made, 1);
        if (connect(sock, (struct sockaddr *)&raddr, sizeof(raddr)) < 0) {
            atomic_fetch_add(&global_stats.calls_failed, 1);
            char prefix[64];
            snprintf(prefix, sizeof(prefix), "Thread %d: Call to %s failed", thread_id, target_addr);
            show_cause_x25(sock, prefix);
            close(sock);
            usleep(cfg.backoff_ms * 1000);
            continue;
        }

        record_facilities(sock);
        
        int call_id = calls_count++;
        int data_len = (rand() % cfg.buffer_size) + 1;
        send_buf[0] = 0x00; // X.25 control byte: Data
        fill_buffer(send_buf + 1, data_len, thread_id, call_id);

        ssize_t sent = write(sock, send_buf, data_len + 1);
        if (sent > 0) {
            atomic_fetch_add(&global_stats.bytes_sent, sent - 1);
            atomic_fetch_add(&global_stats.packets_sent, 1);
            
            int received = 0;
            while (received < sent) {
                ssize_t n = read(sock, recv_buf + received, sent - received);
                if (n <= 0) break;
                received += n;
            }

            if (received > 0) {
                atomic_fetch_add(&global_stats.bytes_received, received - 1);
                atomic_fetch_add(&global_stats.packets_received, 1);
            }

            if (received < sent) {
                atomic_fetch_add(&global_stats.data_mismatches, 1);
                printf("Thread %d: Short receive: expected %d, got %d\n", thread_id, (int)sent, received);
            } else {
                // Skip control byte (index 0) when comparing
                for (int i = 1; i < sent; i++) {
                    if (send_buf[i] != recv_buf[i]) {
                        atomic_fetch_add(&global_stats.data_mismatches, 1);
                        printf("Thread %d: Data mismatch at offset %d (expected 0x%02x, got 0x%02x)\n", 
                                thread_id, i - 1, send_buf[i], recv_buf[i]);
                        break;
                    }
                }
            }
        } else if (sent < 0) {
            perror("write");
        }

        close(sock);
    }

    free(send_buf);
    free(recv_buf);
    return NULL;
}

typedef struct {
    int client_sock;
    struct sockaddr_x25 raddr;
} client_info_t;

void *handle_client(void *arg) {
    client_info_t *info = (client_info_t *)arg;
    int client_sock = info->client_sock;
    unsigned char *buf = malloc(cfg.buffer_size + 1);
    
    record_facilities(client_sock);

    while (1) {
        ssize_t n = read(client_sock, buf, cfg.buffer_size + 1);
        if (n <= 0) break;
        
        // The first byte is the X.25 control byte (usually 0x00 for data)
        // We count only the actual user data
        ssize_t user_data_len = n - 1;
        if (user_data_len < 0) user_data_len = 0;

        atomic_fetch_add(&global_stats.bytes_received, user_data_len);
        atomic_fetch_add(&global_stats.packets_received, 1);

        // Echo back (including the control byte)
        ssize_t sent = write(client_sock, buf, n);
        if (sent > 0) {
            ssize_t sent_user_data = sent - 1;
            if (sent_user_data < 0) sent_user_data = 0;
            atomic_fetch_add(&global_stats.bytes_sent, sent_user_data);
            atomic_fetch_add(&global_stats.packets_sent, 1);
        }
    }
    close(client_sock);
    free(buf);
    free(info);
    return NULL;
}

void receiver_mode() {
    int sock = socket(AF_X25, SOCK_SEQPACKET, 0);
    if (sock < 0) {
        perror("socket");
        return;
    }

    struct sockaddr_x25 laddr;
    memset(&laddr, 0, sizeof(laddr));
    laddr.sx25_family = AF_X25;
    if (strlen(cfg.local_addr) > 0) {
        strncpy(laddr.sx25_addr.x25_addr, cfg.local_addr, X25_ADDR_LEN - 1);
    }
    
    if (bind(sock, (struct sockaddr *)&laddr, sizeof(laddr)) < 0) {
        perror("bind");
        close(sock);
        return;
    }

    // Set facilities for receiver
    struct x25_facilities facilities;
    memset(&facilities, 0, sizeof(facilities));
    facilities.winsize_in = cfg.window_size;
    facilities.winsize_out = cfg.window_size;
    int pac_log = get_pacsize_log(cfg.packet_size);
    facilities.pacsize_in = pac_log;
    facilities.pacsize_out = pac_log;
    if (ioctl(sock, SIOCX25SFACILITIES, &facilities) < 0) {
        perror("ioctl(SIOCX25SFACILITIES)");
    }

    if (listen(sock, 5) < 0) {
        perror("listen");
        close(sock);
        return;
    }

    printf("Receiver listening for X.25 calls...\n");

    while (1) {
        struct sockaddr_x25 raddr;
        socklen_t rlen = sizeof(raddr);
        int client_sock = accept(sock, (struct sockaddr *)&raddr, &rlen);
        if (client_sock < 0) {
            perror("accept");
            continue;
        }

        atomic_fetch_add(&global_stats.calls_received, 1);
        
        client_info_t *info = malloc(sizeof(client_info_t));
        info->client_sock = client_sock;
        info->raddr = raddr;
        
        pthread_t tid;
        if (pthread_create(&tid, NULL, handle_client, info) != 0) {
            perror("pthread_create");
            close(client_sock);
            free(info);
            continue;
        }
        pthread_detach(tid);
    }
}

void print_summary(double duration) {
    printf("\n--- Stress Test Summary ---\n");
    printf("Run Time: %.2f seconds\n", duration);
    printf("Calls Made: %ld\n", atomic_load(&global_stats.calls_made));
    printf("Calls Received: %ld\n", atomic_load(&global_stats.calls_received));
    printf("Calls Failed: %ld\n", atomic_load(&global_stats.calls_failed));
    printf("Packets Sent: %ld\n", atomic_load(&global_stats.packets_sent));
    printf("Packets Received: %ld\n", atomic_load(&global_stats.packets_received));
    printf("Bytes Sent: %ld\n", atomic_load(&global_stats.bytes_sent));
    printf("Bytes Received: %ld\n", atomic_load(&global_stats.bytes_received));
    printf("Data Mismatches: %ld\n", atomic_load(&global_stats.data_mismatches));
    
    int min_pin = atomic_load(&global_stats.min_pacsize_in);
    int max_pin = atomic_load(&global_stats.max_pacsize_in);
    int min_pout = atomic_load(&global_stats.min_pacsize_out);
    int max_pout = atomic_load(&global_stats.max_pacsize_out);
    int min_win = atomic_load(&global_stats.min_winsize_in);
    int max_win = atomic_load(&global_stats.max_winsize_in);
    int min_wout = atomic_load(&global_stats.min_winsize_out);
    int max_wout = atomic_load(&global_stats.max_winsize_out);

    printf("Packet Size Negotiated (In):  Min: %d, Max: %d\n", 
           min_pin == INT_MAX ? 0 : min_pin, max_pin);
    printf("Packet Size Negotiated (Out): Min: %d, Max: %d\n", 
           min_pout == INT_MAX ? 0 : min_pout, max_pout);
    printf("Window Size Negotiated (In):  Min: %d, Max: %d\n", 
           min_win == INT_MAX ? 0 : min_win, max_win);
    printf("Window Size Negotiated (Out): Min: %d, Max: %d\n", 
           min_wout == INT_MAX ? 0 : min_wout, max_wout);
    
    if (duration > 0) {
        printf("Average Bandwidth (Sent): %.2f KB/s\n", (atomic_load(&global_stats.bytes_sent) / 1024.0) / duration);
        printf("Average Bandwidth (Recv): %.2f KB/s\n", (atomic_load(&global_stats.bytes_received) / 1024.0) / duration);
    }
    printf("---------------------------\n");
}

int main(int argc, char *argv[]) {
    cfg.num_threads = 1;
    cfg.buffer_size = 8192;
    strncpy(cfg.start_addr, "127100", X25_ADDR_LEN - 1);
    strncpy(cfg.end_addr, "127300", X25_ADDR_LEN - 1);
    memset(cfg.local_addr, 0, X25_ADDR_LEN - 1);
    cfg.backoff_ms = 1000;
    cfg.run_time_s = 10;
    cfg.max_calls = 100;
    cfg.is_receiver = false;
    cfg.window_size = 4;
    cfg.packet_size = 512;

    atomic_init(&global_stats.min_pacsize_in, INT_MAX);
    atomic_init(&global_stats.min_pacsize_out, INT_MAX);
    atomic_init(&global_stats.min_winsize_in, INT_MAX);
    atomic_init(&global_stats.min_winsize_out, INT_MAX);

    int opt;
    while ((opt = getopt(argc, argv, "N:l:d:b:T:n:ra:W:P:")) != -1) {
        switch (opt) {
            case 'N': cfg.num_threads = atoi(optarg); break;
            case 'l': cfg.buffer_size = atoi(optarg); break;
            case 'a': strncpy(cfg.local_addr, optarg, X25_ADDR_LEN - 1); break;
            case 'W': cfg.window_size = atoi(optarg); break;
            case 'P': cfg.packet_size = atoi(optarg); break;
            case 'd': {
                char *comma = strchr(optarg, ',');
                if (comma) {
                    *comma = '\0';
                    strncpy(cfg.start_addr, optarg, X25_ADDR_LEN - 1);
                    strncpy(cfg.end_addr, comma + 1, X25_ADDR_LEN - 1);
                }
                break;
            }
            case 'b': cfg.backoff_ms = atoi(optarg); break;
            case 'T': cfg.run_time_s = atoi(optarg); break;
            case 'n': cfg.max_calls = atoi(optarg); break;
            case 'r': cfg.is_receiver = true; break;
            default:
                fprintf(stderr, "Usage: %s [-N threads] [-l bufsize] [-d start,end] [-b backoff_ms] [-T runtime] [-n maxcalls] [-r (receiver mode)] [-a local_addr] [-W window_size] [-P packet_size]\n", argv[0]);
                exit(EXIT_FAILURE);
        }
    }

    srand(time(NULL));

    gettimeofday(&global_start_total, NULL);
    signal(SIGINT, sigint_handler);
    signal(SIGPIPE, SIG_IGN);

    if (cfg.is_receiver) {
        receiver_mode();
    } else {
        pthread_t *threads = malloc(sizeof(pthread_t) * cfg.num_threads);
        for (int i = 0; i < cfg.num_threads; i++) {
            pthread_create(&threads[i], NULL, sender_thread, (void *)(long)i);
        }
        for (int i = 0; i < cfg.num_threads; i++) {
            pthread_join(threads[i], NULL);
        }
        free(threads);
    }

    struct timeval end_total;
    gettimeofday(&end_total, NULL);
    double duration = (end_total.tv_sec - global_start_total.tv_sec) + (end_total.tv_usec - global_start_total.tv_usec) / 1000000.0;
    
    print_summary(duration);

    return 0;
}
