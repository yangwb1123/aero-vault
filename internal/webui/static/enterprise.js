function accessValue(id) { return document.getElementById(id).value; }
function objectPath() { return '/v1/files/' + encodePath(accessValue('access-key')); }

async function downloadObject() {
  const response = await fetch(objectPath(), { headers: headers() });
  if (!response.ok) { document.getElementById('access-result').textContent = await response.text(); return; }
  const blob = await response.blob();
  const anchor = document.createElement('a');
  anchor.href = URL.createObjectURL(blob);
  anchor.download = accessValue('access-key').split('/').pop() || 'download';
  anchor.click();
  URL.revokeObjectURL(anchor.href);
}

async function createPresignedGet() {
  await accessJSON(objectPath() + '/presign?op=get&expires=900', 'POST');
}

async function deleteObject() {
  if (!window.confirm('Soft-delete this object?')) return;
  await accessJSON(objectPath(), 'DELETE');
  refresh();
}

async function restoreObject() {
  await accessJSON(objectPath() + '/restore', 'POST');
  refresh();
}

async function createShare() {
  await accessJSON('/v1/shares', 'POST', {
    key: accessValue('access-key'), password: accessValue('share-password'),
    allow_preview: true, allow_download: true,
    ttl_seconds: Number(accessValue('share-ttl') || 0)
  });
}

async function listShares() {
  await accessJSON('/v1/shares?bucket=default&key=' + encodeURIComponent(accessValue('access-key')), 'GET');
}

async function revokeShare() {
  await accessJSON('/v1/shares/' + encodeURIComponent(accessValue('share-id')), 'DELETE');
  await listShares();
}

async function publishAsset() {
  await accessJSON('/v1/assets', 'POST', {
    key: accessValue('access-key'), slug: accessValue('asset-slug'),
    cache_control: 'public, max-age=86400'
  });
}

async function listAssets() { await accessJSON('/v1/assets', 'GET'); }

async function unpublishAsset() {
  await accessJSON('/v1/assets/' + encodePath(accessValue('asset-slug')), 'DELETE');
  await listAssets();
}

async function createDepartment() {
  await accessJSON('/v1/admin/departments', 'POST', {
    name: accessValue('dept-name'), parent_id: accessValue('dept-parent-id')
  });
}

async function listDepartments() { await accessJSON('/v1/admin/departments', 'GET'); }

async function getDepartment() {
  await accessJSON('/v1/admin/departments/' + encodeURIComponent(accessValue('dept-id')), 'GET');
}

async function deleteDepartment() {
  if (!window.confirm('Delete this department and its descendants?')) return;
  await accessJSON('/v1/admin/departments/' + encodeURIComponent(accessValue('dept-id')), 'DELETE');
  await listDepartments();
}

function departmentMemberPath() {
  return '/v1/admin/departments/' + encodeURIComponent(accessValue('member-dept-id')) +
    '/members/' + encodeURIComponent(accessValue('member-subject-id'));
}

async function putDepartmentMember() {
  await accessJSON(departmentMemberPath(), 'PUT', { role: 'member' });
}

async function deleteDepartmentMember() {
  await accessJSON(departmentMemberPath(), 'DELETE');
}

function resourceACLQuery() {
  return '?bucket=default&key=' + encodeURIComponent(accessValue('access-key')) +
    '&kind=' + encodeURIComponent(accessValue('acl-kind'));
}

async function putResourceACL() {
  await accessJSON('/v1/access/acl', 'PUT', {
    key: accessValue('access-key'), resource_kind: accessValue('acl-kind'),
    principal_type: accessValue('acl-principal'), principal_id: accessValue('acl-principal-id'),
    actions: accessValue('acl-actions').split(',').map(x => x.trim()).filter(Boolean),
    effect: accessValue('acl-effect'), inherit: document.getElementById('acl-inherit').checked
  });
}

async function listResourceACL() { await accessJSON('/v1/access/acl' + resourceACLQuery(), 'GET'); }

async function deleteResourceACL() {
  await accessJSON('/v1/access/acl/' + encodeURIComponent(accessValue('acl-id')), 'DELETE');
}

async function exportBackup() {
  const response = await fetch('/v1/exports/archive?prefix=' + encodeURIComponent(accessValue('export-prefix')),
    { headers: headers() });
  if (!response.ok) { document.getElementById('access-result').textContent = await response.text(); return; }
  const blob = await response.blob(); const anchor = document.createElement('a');
  anchor.href = URL.createObjectURL(blob); anchor.download = 'aero-backup.tar.gz'; anchor.click();
  URL.revokeObjectURL(anchor.href);
}
