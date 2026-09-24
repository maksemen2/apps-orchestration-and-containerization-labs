#!/usr/bin/env bash
set -euo pipefail

API=$(realpath -e -- "${1:?Передай путь к lab-server}")
FILTER=$(realpath -e -- ./seccomp-no-mkdir.bpf)
CG=/sys/fs/cgroup/mydocker-lab

# Создать группу и установить лимиты.
sudo mkdir -- "$CG"

echo 67108864       | sudo tee "$CG/memory.max" >/dev/null
echo 0              | sudo tee "$CG/memory.swap.max" >/dev/null
echo '50000 100000' | sudo tee "$CG/cpu.max" >/dev/null
echo 64             | sudo tee "$CG/pids.max" >/dev/null

# Перенести скрипт; будущие дочерние процессы унаследуют группу.
echo "$$" | sudo tee "$CG/cgroup.procs" >/dev/null

# Создать namespaces и настроить окружение.
exec unshare \
    --user --map-root-user \
    --pid --fork \
    --mount --mount-proc \
    --uts --net --ipc \
    bash --noprofile --norc -c '
        set -euo pipefail

        hostname labbox
        ip link set lo up

        exec setpriv \
            --bounding-set=-all \
            --inh-caps=-all \
            --ambient-caps=-all \
            --no-new-privs \
            --seccomp-filter "$2" \
            "$1"
    ' mydocker-init "$API" "$FILTER"