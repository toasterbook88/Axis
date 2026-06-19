import os
import pty
import sys
import time
import select

def run_smoke_test():
    # Spawn child process in pseudo-terminal
    pid, fd = pty.fork()

    if pid == 0:
        # Child process
        os.execvp("./axis", ["./axis", "agent", "--no-color"])
        sys.exit(0)

    # Parent process
    output = []
    
    def read_all():
        time.sleep(0.1)
        res = b""
        while True:
            r, _, _ = select.select([fd], [], [], 0.05)
            if not r:
                break
            try:
                chunk = os.read(fd, 1024)
                if not chunk:
                    break
                res += chunk
            except OSError:
                break
        if res:
            sys.stdout.buffer.write(res)
            sys.stdout.flush()
            output.append(res.decode('utf-8', errors='ignore'))
        return res

    # Wait for agent prompt
    print("--- Start of PTY Smoke Test ---")
    time.sleep(1.0)
    read_all()

    # 1. Send "/models\n"
    print("\n>>> Sending: /models")
    os.write(fd, b"/models\n")
    time.sleep(0.5)
    read_all()

    # 2. Send Down arrow (\x1b[B)
    print("\n>>> Sending: Down arrow")
    os.write(fd, b"\x1b[B")
    time.sleep(0.2)
    read_all()

    # 3. Send Enter (\n)
    print("\n>>> Sending: Enter")
    os.write(fd, b"\n")
    time.sleep(0.5)
    read_all()

    # 4. Send bare "/model\n"
    print("\n>>> Sending: bare /model")
    os.write(fd, b"/model\n")
    time.sleep(0.5)
    read_all()

    # 5. Send Escape (\x1b) to dismiss
    print("\n>>> Sending: Escape")
    os.write(fd, b"\x1b")
    time.sleep(0.5)
    read_all()

    # 6. Send "/mcp\n"
    print("\n>>> Sending: /mcp")
    os.write(fd, b"/mcp\n")
    time.sleep(0.5)
    read_all()

    # 7. Select the first server by pressing Enter (\n)
    print("\n>>> Sending: Enter (select server)")
    os.write(fd, b"\n")
    time.sleep(0.5)
    read_all()

    # 8. Send Down arrow 3 times to highlight "Back"
    print("\n>>> Sending: Down arrow 3 times to highlight 'Back'")
    os.write(fd, b"\x1b[B\x1b[B\x1b[B")
    time.sleep(0.2)
    read_all()

    # 9. Send Enter (\n) to trigger Back
    print("\n>>> Sending: Enter (Back)")
    os.write(fd, b"\n")
    time.sleep(0.5)
    read_all()

    # 10. Send Escape (\x1b) to exit MCP menu
    print("\n>>> Sending: Escape")
    os.write(fd, b"\x1b")
    time.sleep(0.5)
    read_all()

    # 11. Send Ctrl+C (\x03) to quit agent
    print("\n>>> Sending: Ctrl+C")
    os.write(fd, b"\x03")
    time.sleep(1.0)
    read_all()

    print("\n--- End of PTY Smoke Test ---")

if __name__ == "__main__":
    run_smoke_test()
