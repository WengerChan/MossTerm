/**
 * SessionForm —— Profile 编辑/新建表单
 * --------------------------------------------------------------------
 * 在 Modal 中渲染，提交时通过 connectionStore.saveProfile 持久化。
 *
 * 字段（与 Profile DTO 对齐）：
 *   - 基础：name / group / host / port / user / protocol
 *   - 认证：kind + （password | keyId | passphrase）
 *   - 高级：env / jumpVia / tags / color / icon
 */
import { useState } from "react";
import { Save, X, Key, Lock, User2, Cpu } from "lucide-react";
import { useSessionStore } from "./sessionStore";
import { useConnectionStore } from "@stores/connectionStore";
import { useUIStore } from "@stores/uiStore";
import { Button } from "@components/common/Button";
import type { AuthKind, Profile } from "@/types/session";

const AUTH_OPTIONS: { kind: AuthKind; label: string; icon: React.ReactNode }[] = [
  { kind: "password",             label: "密码",   icon: <Lock size={12} /> },
  { kind: "publickey",            label: "公私钥", icon: <Key size={12} /> },
  { kind: "agent",                label: "ssh-agent", icon: <User2 size={12} /> },
  { kind: "keyboard-interactive", label: "键盘交互", icon: <Cpu size={12} /> },
];

export interface SessionFormProps {
  /** 编辑已有 profile 时传入；新建留空 */
  profileId?: string;
  onClose?: () => void;
}

export function SessionForm({ profileId, onClose }: SessionFormProps): JSX.Element {
  const draft       = useSessionStore((s) => s.form.draft);
  const dirty       = useSessionStore((s) => s.form.dirty);
  const updateDraft = useSessionStore((s) => s.updateDraft);
  const updateAuth  = useSessionStore((s) => s.updateAuth);
  const resetForm   = useSessionStore((s) => s.resetForm);
  const saveProfile = useConnectionStore((s) => s.saveProfile);
  const pushToast   = useUIStore((s) => s.pushToast);

  const [showAdvanced, setShowAdvanced] = useState(false);

  const handleSubmit = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault();
    if (!draft.name.trim()) {
      pushToast({ level: "warn", message: "请填写名称", durationMs: 2000 });
      return;
    }
    const p: Profile = {
      ...draft,
      id: profileId ?? draft.id,
      updatedAt: Date.now(),
    };
    await saveProfile(p);
    pushToast({ level: "success", message: "已保存", durationMs: 2000 });
    resetForm();
    onClose?.();
  };

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-3 p-4 text-xs">
      {/* 基础字段 */}
      <div className="grid grid-cols-2 gap-2">
        <Field label="名称">
          <input
            className="moss-input"
            value={draft.name}
            onChange={(e) => updateDraft({ name: e.target.value })}
            placeholder="生产 Web 服务器"
          />
        </Field>
        <Field label="分组">
          <input
            className="moss-input"
            value={draft.group ?? ""}
            onChange={(e) => updateDraft({ group: e.target.value })}
            placeholder="默认"
          />
        </Field>
      </div>

      <div className="grid grid-cols-[1fr_80px_1fr] gap-2">
        <Field label="主机">
          <input
            className="moss-input"
            value={draft.host}
            onChange={(e) => updateDraft({ host: e.target.value })}
            placeholder="192.168.1.10"
          />
        </Field>
        <Field label="端口">
          <input
            className="moss-input"
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(e) => updateDraft({ port: parseInt(e.target.value, 10) || 22 })}
          />
        </Field>
        <Field label="用户">
          <input
            className="moss-input"
            value={draft.user}
            onChange={(e) => updateDraft({ user: e.target.value })}
            placeholder="root"
          />
        </Field>
      </div>

      {/* 认证方式 */}
      <Field label="认证方式">
        <div className="flex gap-1">
          {AUTH_OPTIONS.map((opt) => {
            const selected = draft.auth.kind === opt.kind;
            return (
              <button
                key={opt.kind}
                type="button"
                onClick={() => updateAuth({ kind: opt.kind })}
                className={
                  "flex items-center gap-1 rounded border px-2 py-1 text-[11px] transition-colors " +
                  (selected
                    ? "border-accent bg-accent/15 text-accent"
                    : "border-moss-border bg-moss-bg text-ink-muted hover:bg-moss-hover hover:text-ink")
                }
              >
                {opt.icon}
                {opt.label}
              </button>
            );
          })}
        </div>
      </Field>

      {/* 认证细节：按 kind 切换表单 */}
      {draft.auth.kind === "password" && (
        <Field label="密码">
          <input
            type="password"
            className="moss-input"
            value={draft.auth.username ?? ""}
            onChange={(e) => updateAuth({ username: e.target.value })}
            placeholder="（留空使用 keyring 凭据）"
          />
        </Field>
      )}
      {draft.auth.kind === "publickey" && (
        <>
          <Field label="私钥 ID">
            <input
              className="moss-input"
              value={draft.auth.keyId ?? ""}
              onChange={(e) => updateAuth({ keyId: e.target.value })}
              placeholder="从 secret store 选"
            />
          </Field>
          <Field label="Passphrase">
            <input
              type="password"
              className="moss-input"
              onChange={(e) => updateAuth({ command: e.target.value })}
              placeholder="（可选）"
            />
          </Field>
        </>
      )}
      {draft.auth.kind === "agent" && (
        <div className="text-ink-muted">将使用系统 ssh-agent 内的私钥</div>
      )}
      {draft.auth.kind === "keyboard-interactive" && (
        <div className="text-ink-muted">连接时弹出交互式提问</div>
      )}

      {/* 高级 */}
      <button
        type="button"
        onClick={() => setShowAdvanced((v) => !v)}
        className="self-start text-[10px] text-ink-muted hover:text-ink"
      >
        {showAdvanced ? "▾ 收起高级" : "▸ 高级选项"}
      </button>
      {showAdvanced && (
        <div className="grid grid-cols-2 gap-2 rounded border border-moss-border bg-moss-bg p-2">
          <Field label="标签（逗号分隔）">
            <input
              className="moss-input"
              value={draft.tags?.join(",") ?? ""}
              onChange={(e) =>
                updateDraft({ tags: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })
              }
            />
          </Field>
          <Field label="颜色（hex）">
            <input
              className="moss-input"
              value={draft.color ?? ""}
              onChange={(e) => updateDraft({ color: e.target.value })}
              placeholder="#7CB342"
            />
          </Field>
        </div>
      )}

      {/* 操作 */}
      <div className="mt-2 flex items-center justify-end gap-2">
        <Button
          size="sm"
          icon={<X size={12} />}
          onClick={() => { resetForm(); onClose?.(); }}
          type="button"
        >
          取消
        </Button>
        <Button
          size="sm"
          variant="primary"
          icon={<Save size={12} />}
          type="submit"
          disabled={!dirty}
        >
          保存
        </Button>
      </div>
    </form>
  );
}

interface FieldProps {
  label: string;
  children: React.ReactNode;
}
function Field({ label, children }: FieldProps): JSX.Element {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[10px] uppercase tracking-wider text-ink-subtle">{label}</span>
      {children}
    </label>
  );
}
