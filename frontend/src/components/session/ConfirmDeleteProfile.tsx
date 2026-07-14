/**
 * ConfirmDeleteProfile —— 删除 profile 确认弹窗
 * --------------------------------------------------------------------
 * v0.5.6：SessionTree 右键/hover 删除时 openModal({ id: CONFIRM_DELETE_MODAL_ID, props: { profileId } })，
 * 本组件检测 id 匹配后渲染确认 + 调 connectionStore.deleteProfile。
 *
 * 设计：
 *   - 不用 useUIStore 里的 props 透传（v0.5.5 store 已经有 props?: Record<string, unknown>）
 *   - 但直接读 uiStore.modal.props.profileId 简单
 *   - 用 Toast 反馈成功/失败
 */
import { useState } from "react";
import { Trash2, X } from "lucide-react";
import { Modal } from "@components/common/Modal";
import { Button } from "@components/common/Button";
import { useUIStore } from "@stores/uiStore";
import { useConnectionStore } from "@stores/connectionStore";

export const CONFIRM_DELETE_MODAL_ID = "confirm-delete-profile";

export function ConfirmDeleteProfile(): JSX.Element | null {
  const modal      = useUIStore((s) => s.modal);
  const closeModal = useUIStore((s) => s.closeModal);
  const pushToast  = useUIStore((s) => s.pushToast);
  const deleteProfile = useConnectionStore((s) => s.deleteProfile);
  const getProfile = useConnectionStore((s) => s.getProfile);
  const [deleting, setDeleting] = useState(false);

  if (modal?.id !== CONFIRM_DELETE_MODAL_ID) return null;

  // profileId 透传：openModal({ props: { profileId } }) 时从 modal.props 拿
  const profileId = (modal.props?.profileId as string) ?? "";
  const profile   = getProfile(profileId);
  const name      = profile?.name ?? profileId;

  const handleDelete = async (): Promise<void> => {
    if (!profileId || deleting) return;
    setDeleting(true);
    try {
      await deleteProfile(profileId);
      pushToast({ level: "success", message: `已删除 profile "${name}"`, durationMs: 2000 });
      closeModal();
    } catch (err: unknown) {
      pushToast({ level: "error", message: `删除失败：${String(err)}`, durationMs: 3000 });
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Modal
      id={CONFIRM_DELETE_MODAL_ID}
      title={`删除 profile "${name}"?`}
      width="min(440px, 90vw)"
      dismissOnBackdrop={!deleting}
    >
      <div className="flex flex-col gap-3 p-4 text-xs">
        <div className="flex items-start gap-2">
          <Trash2 size={14} className="mt-0.5 shrink-0 text-state-err" />
          <div className="flex-1 text-ink">
            <p>确定要删除 profile <span className="font-semibold text-ink">{name}</span> 吗？</p>
            <p className="mt-2 text-ink-muted">
              该操作会从 config 文件移除该 profile，<span className="font-semibold text-state-err">不可恢复</span>。
              已打开的 session 不受影响，但下次开新 session 找不到这个 profile。
            </p>
          </div>
        </div>
        <div className="mt-2 flex items-center justify-end gap-2">
          <Button
            size="sm"
            icon={<X size={12} />}
            onClick={closeModal}
            type="button"
            disabled={deleting}
          >
            取消
          </Button>
          <Button
            size="sm"
            variant="danger"
            icon={<Trash2 size={12} />}
            onClick={handleDelete}
            type="button"
            disabled={deleting}
          >
            {deleting ? "删除中..." : "确认删除"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
