/**
 * ProfileEditModal —— Profile 编辑/新建 modal 容器
 * --------------------------------------------------------------------
 * v0.5.6：包 SessionForm 进 Modal 容器，监听 uiStore.modal.id == PROFILE_EDIT_MODAL_ID
 * 决定 open/close。
 *
 * 触发链：
 *   - Sidebar "+" 按钮 / SessionTree 编辑按钮 → sessionStore.startCreate / startEdit
 *   - sessionStore 内部调 openModal({ id: PROFILE_EDIT_MODAL_ID })
 *   - 本 modal 检测到 id 匹配 → open
 *   - SessionForm 保存 → saveProfile → onClose → closeModal + resetForm
 *
 * 关键设计：
 *   - **不进** useEffect 自动 openModal —— startCreate / startEdit 已经做了（单一来源）
 *   - title 由 uiStore.modal.title 决定（store 里已带"新建"/"编辑"标签）
 *   - 关闭后调 resetForm 清空 draft，避免下次 open 时看到陈旧数据
 */
import { Modal } from "@components/common/Modal";
import { useSessionStore } from "./sessionStore";
import { useUIStore } from "@stores/uiStore";
import { SessionForm } from "./SessionForm";

export const PROFILE_EDIT_MODAL_ID = "profile-edit";

export function ProfileEditModal(): JSX.Element | null {
  const modal       = useUIStore((s) => s.modal);
  const closeModal  = useUIStore((s) => s.closeModal);
  const resetForm   = useSessionStore((s) => s.resetForm);

  // Modal 组件自己根据 id 匹配决定 render —— 这层只是 wrapper
  if (modal?.id !== PROFILE_EDIT_MODAL_ID) return null;

  return (
    <Modal
      id={PROFILE_EDIT_MODAL_ID}
      title={modal.title}
      width="min(720px, 95vw)"
      dismissOnBackdrop={false}
    >
      <SessionForm onClose={() => { resetForm(); closeModal(); }} />
    </Modal>
  );
}
