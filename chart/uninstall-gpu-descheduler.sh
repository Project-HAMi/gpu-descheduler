#!/bin/bash
# ============================================
# GPU Descheduler Webhook Uninstall Script
# Cleans up all related resources, supports re-deployment
# ============================================

set -e

echo "=============================================="
echo "GPU Descheduler Webhook Uninstall Tool"
echo "=============================================="
echo ""

# Step 1: Uninstall Helm Release
echo "Step 1: Uninstalling Helm Release..."
if helm status gpu-descheduler -n kube-system >/dev/null 2>&1; then
    helm uninstall gpu-descheduler -n kube-system --wait
    echo "   Helm Release uninstalled"
else
    echo "   Helm Release not found (skipped)"
fi

# Step 2: Delete TLS Secret
echo ""
echo "Step 2: Deleting TLS Secret..."
if kubectl get secret gpu-descheduler-webhook-tls -n kube-system >/dev/null 2>&1; then
    kubectl delete secret gpu-descheduler-webhook-tls -n kube-system
    echo "   TLS Secret deleted"
else
    echo "   TLS Secret not found (skipped)"
fi

# Step 3: Delete CRD
echo ""
echo "Step 3: Deleting CRD..."
if kubectl get crd hamipodplacements.scheduling.hami.placement.com >/dev/null 2>&1; then
    kubectl delete crd hamipodplacements.scheduling.hami.placement.com
    echo "   CRD deleted"
else
    echo "   CRD not found (skipped)"
fi

# Step 4: Delete Leader Election Lease
echo ""
echo "Step 4: Deleting Leader Lease..."
if kubectl get lease gpu-descheduler.hami.io -n kube-system >/dev/null 2>&1; then
    kubectl delete lease gpu-descheduler.hami.io -n kube-system
    echo "   Leader Lease deleted"
else
    echo "   Leader Lease not found (skipped)"
fi

# Step 5: Clean up residual Pods
echo ""
echo "Step 5: Cleaning up residual Pods..."
POD_COUNT=$(kubectl get pod -n kube-system -l app=gpu-descheduler-webhook --no-headers 2>/dev/null | wc -l)
if [ "$POD_COUNT" -gt 0 ]; then
    kubectl delete pod -n kube-system -l app=gpu-descheduler-webhook --force --grace-period=0 2>/dev/null || true
    sleep 2
    echo "   Residual Pods cleaned up"
else
    echo "   No residual Pods (skipped)"
fi

# Step 6: Verify cleanup
echo ""
echo "Step 6: Verifying cleanup..."
sleep 3

REMAINING_PODS=$(kubectl get all -n kube-system -l app=gpu-descheduler-webhook 2>/dev/null | grep -v "^$" | tail -n +2 | wc -l)
REMAINING_SECRETS=$(kubectl get secret -n kube-system | grep gpu-descheduler | wc -l)
REMAINING_CRDS=$(kubectl get crd | grep hamipodplacements | wc -l)

if [ "$REMAINING_PODS" -eq 0 ] && [ "$REMAINING_SECRETS" -eq 0 ] && [ "$REMAINING_CRDS" -eq 0 ]; then
    echo "   All resources cleaned up"
    EXIT_CODE=0
else
    echo "   Some resources may remain:"
    [ "$REMAINING_PODS" -gt 0 ] && echo "      - Pods: $REMAINING_PODS"
    [ "$REMAINING_SECRETS" -gt 0 ] && echo "      - Secrets: $REMAINING_SECRETS"
    [ "$REMAINING_CRDS" -gt 0 ] && echo "      - CRDs: $REMAINING_CRDS"
    EXIT_CODE=1
fi

echo ""
echo "=============================================="
if [ $EXIT_CODE -eq 0 ]; then
    echo "Uninstall complete! Environment is clean"
    echo ""
    echo "You can now run the install script:"
    echo "   ./install-gpu-descheduler.sh"
else
    echo "Uninstall complete, but some resources remain"
    echo ""
    echo "Please check manually:"
    echo "   kubectl get all -n kube-system -l app=gpu-descheduler-webhook"
fi
echo "=============================================="

exit $EXIT_CODE