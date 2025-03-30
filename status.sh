docker exec test_mm bash -c 'until mmctl --local system status 2> /dev/null; do echo "waiting for server to become available"; sleep 5; done'
