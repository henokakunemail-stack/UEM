# Fase 16 — Architectural Blueprint: Active Directory Group Policy Engine (`systempolicy`)

**Status:** IMPLEMENTED & VERIFIED  
**Tanggal:** 2026-10-13  
**Arsitek:** Principal Systems Architect & Security Engineering Specialist  

---

## 1. Analisis Kebutuhan & Spesifikasi Enterprise

Dalam lingkungan enterprise, pengelolaan konfigurasi keamanan endpoint yang setara dengan Active Directory Group Policy Objects (GPO) sangat penting tanpa memerlukan domain controller Windows On-Premises. Modul **System Policy Engine** (`systempolicy`) menyediakan tata kelola kebijakan tersentralisasi untuk armada lintas OS (Windows, Linux, macOS).

Fitur utama:
1. **Hirarki Prioritas Kebijakan Berlapis (Multi-Tier Inheritance)**:
   - `all` (Fleet Baseline, priority terendah)
   - `group` (Policy spesifik Department/Site)
   - `device` (Policy override khusus satu endpoint, prioritas tertinggi)
2. **Kontrol Keamanan Endpoint**:
   - `usb_storage_mode`: `allow`, `read_only`, atau `block` (enforce via registry `SYSTEM\CurrentControlSet\Services\USBSTOR`).
   - `disable_cmd`: Blokir Command Prompt (`DisableCMD`).
   - `disable_task_mgr`: Blokir Task Manager (`DisableTaskMgr`).
   - `disable_registry_tools`: Blokir Regedit (`DisableRegistryTools`).
   - `disable_control_panel`: Sembunyikan Control Panel & Windows Settings (`NoControlPanel`).
   - `windows_update_mode`: AUOptions (`default`, `notify_download`, `auto_download_notify_install`, `auto_install_schedule`).
   - `wallpaper_enabled` & `wallpaper_style`: Enforce wallpaper korporat via registry atau GNOME `gsettings`.
   - `inactivity_timeout_minutes`: Kunci layar otomatis setelah idle time tertentu.
3. **Mekanisme Distribusi & Event Loop**:
   - Auto-dispatch realtime saat policy dibuat/diubah/dihapus via WebSocket command `system_policy.apply`.
   - Sync on reconnect: jika status perangkat belum `synced` pada versi policy terbaru, server otomatis mengirim ulang kebijakan saat agent terhubung kembali.
   - Telemetry reporting: Agent melaporkan hasil penerapan via `POST /api/agent/devices/{id}/system-policy/report`.
4. **RBAC & Audit**:
   - Mutasi policy mewajibkan peran `admin`.
   - Force sync perangkat dapat dipicu oleh `technician`.
   - Seluruh mutasi dicatat dalam tamper-evident audit log.

---

## 2. Skema Basis Data (`server/core/db/migrations/0024_system_policies.sql`)

```sql
CREATE TABLE IF NOT EXISTS system_policies (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    target_type TEXT NOT NULL DEFAULT 'all', -- 'all', 'group', 'device'
    target_id TEXT NOT NULL DEFAULT '',
    is_enabled BOOLEAN NOT NULL DEFAULT 1,
    priority INTEGER NOT NULL DEFAULT 100,
    settings_json TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS device_system_policy_states (
    device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    policy_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'synced', 'failed'
    applied_settings_json TEXT NOT NULL DEFAULT '{}',
    error_message TEXT NOT NULL DEFAULT '',
    last_synced_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_system_policies_target ON system_policies(target_type, target_id);
```

---

## 3. Kompilasi & Pengujian

- Kompilasi server & agent: `CGO_ENABLED=0`
- Unit tests: `server/modules/systempolicy/...` dan `agent/shared/systempolicy/...`
- E2E Integration: `scripts/e2e-system-policy.ps1` (port 18457).
