#!/usr/bin/env bash

leader="$(kubectl get pod -l app=receiver -o json | jq -r '.items[] | select(all(.status.conditions[]; . as $c | $c.type != "Ready" or $c.status == "True")).metadata.name')"
other="$(kubectl get pod -l app=receiver -o json | jq -r '.items[] | select(all(.status.conditions[]; . as $c | $c.type != "Ready" or $c.status == "False")).metadata.name')"

echo leader: $leader
echo other: $other

echo "Marking leader as non ready"
kubectl exec -c leader-election-udp-0 "$leader" -- bash -c "echo no > /var/run/udplisten/ready"
sleep 3
echo "Making other leader ready"
(kubectl exec -c leader-election-udp-0 "$other" -- bash -c "echo yes > /var/run/udplisten/ready") &
echo "Deleting leader"
kubectl delete --wait=false pod "$leader"
