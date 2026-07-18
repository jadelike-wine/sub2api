export default {
  imageCredentials: {
    title: 'AI 生图凭据',
    description: '管理上游 Agnes API Key 与图片存储配置',
    // 凭据列表
    columns: {
      name: '名称',
      provider: '提供商',
      fingerprint: 'Key 指纹',
      status: '状态',
      priority: '优先级',
      weight: '权重',
      enabled: '启用',
      consecutiveFailures: '连续失败次数',
      lastUsedAt: '上次使用',
      lastSuccessAt: '上次成功',
      lastFailureAt: '上次失败',
      cooldownUntil: '冷却至',
      lastError: '上次错误',
      actions: '操作'
    },
    status: {
      healthy: '健康',
      unhealthy: '异常',
      disabled: '已禁用'
    },
    // 操作按钮
    actions: {
      create: '新建凭据',
      edit: '编辑',
      test: '测试',
      delete: '删除',
      refresh: '刷新',
      cancel: '取消',
      save: '保存'
    },
    // 表单
    form: {
      createTitle: '新建凭据',
      editTitle: '编辑凭据',
      name: '名称',
      namePlaceholder: '给这个凭据起个名字',
      provider: '提供商',
      providerHint: '默认 agnes',
      apiKey: 'API Key',
      apiKeyPlaceholder: '粘贴 Agnes API Key',
      apiKeyHint: '保存后只会保留指纹，明文不会再返回',
      apiKeyKeepHint: '留空表示不更新现有 Key',
      enabled: '启用',
      priority: '优先级',
      priorityHint: '数字越大优先级越高',
      weight: '权重',
      weightHint: '同优先级内的轮询权重',
      errors: {
        nameRequired: '名称不能为空',
        apiKeyRequired: 'API Key 不能为空',
        save: '保存失败'
      }
    },
    // 测试结果
    test: {
      title: '测试结果',
      running: '正在测试...',
      success: '测试成功',
      failure: '测试失败',
      httpStatus: 'HTTP 状态',
      duration: '耗时',
      fingerprint: 'Key 指纹',
      errorCode: '错误码',
      errorMessage: '错误信息',
      retry: '重新测试',
      close: '关闭'
    },
    // 存储状态
    storage: {
      title: '存储配置',
      description: '展示当前图片存储驱动及可用状态',
      configured: '已配置',
      notConfigured: '未配置',
      driver: '存储驱动',
      driverLocal: '本地磁盘',
      driverS3: 'S3',
      bucket: 'Bucket',
      notAvailable: '—',
      hint: '在服务端 config.yaml 或环境变量中配置存储参数（storage_driver: local 或 s3）'
    },
    // 孤立资产清理
    cleanup: {
      title: '孤立资产清理',
      description: '清理"已软删除但存储对象仍存在"的图片资产。后端定时任务会按保留期自动清理，这里可手动触发。',
      filterMode: '筛选方式',
      modeOlderThan: '按天数',
      modeBeforeDate: '按日期',
      olderThanDays: '清理 N 天前',
      olderThanDaysPlaceholder: '7',
      beforeDate: '清理此日期之前',
      beforeDatePlaceholder: '2026-01-01',
      batchSize: '批量大小（可选）',
      batchSizePlaceholder: '留空走配置默认',
      batchSizeHint: '限制单次清理的资产数量，防止一次清理拖垮存储（上限 5000）',
      preview: '预览数量',
      previewing: '查询中...',
      previewResult: '预计清理 {count} 条资产（截止时间：{cutoff}）',
      previewEmpty: '当前条件下没有需要清理的资产',
      previewError: '预览失败',
      execute: '一键清理',
      executing: '清理中...',
      confirmTitle: '确认清理',
      confirmMessage: '将物理删除 {count} 条已软删除的资产及其存储对象，此操作不可恢复。是否继续？',
      confirm: '确认清理',
      cancel: '取消',
      result: {
        title: '清理结果',
        scanned: '扫描数量',
        deletedAssets: '已删除资产',
        deletedStorageObjects: '已删除存储对象',
        storageFailures: '存储删除失败',
        dbFailures: '数据库删除失败',
        durationMs: '耗时（毫秒）',
        cutoff: '截止时间',
        success: '清理完成',
        partial: '清理完成（含失败）',
        close: '关闭'
      },
      errors: {
        execute: '清理失败',
        invalidBeforeDate: '日期格式必须为 RFC3339（如 2026-01-01T00:00:00Z）',
        invalidDays: '天数必须 >= 0',
        futureDate: '日期不能晚于当前时间',
        mutuallyExclusive: '天数与日期只能选其一'
      }
    },
    // 提示消息
    messages: {
      created: '凭据已创建',
      updated: '凭据已更新',
      deleted: '凭据已删除',
      tested: '测试完成',
      confirmDelete: '确定删除该凭据？历史生成记录会保留 provider_credential_id 引用。',
      loadError: '加载凭据失败',
      createError: '创建凭据失败',
      updateError: '更新凭据失败',
      deleteError: '删除凭据失败',
      testError: '测试凭据失败',
      storageError: '加载存储状态失败'
    },
    empty: '暂无凭据，点击「新建凭据」开始配置',
    // 分层价格
    imagePricing: {
      title: 'AI 生图分层价格',
      description: '配置 1K/2K/3K/4K 各尺寸的单张扣费价格（美元）。留空则使用 config.yaml 中的默认值（默认 $0.002/张）。',
      tier1K: '1K 价格',
      tier2K: '2K 价格',
      tier3K: '3K 价格',
      tier4K: '4K 价格',
      tierUnit: '$/张',
      placeholder: '留空使用默认',
      notConfigured: '未配置（使用默认值）',
      save: '保存价格',
      saving: '保存中...',
      saved: '价格已更新',
      saveError: '保存价格失败',
      loadError: '加载价格失败',
      invalidPrice: '价格必须 >= 0',
      preview: '预览：每张 {tier} = ${price}',
      hint: '生成成功后按对应 tier 扣费；失败不扣费。允许用户余额透支。'
    },
    // 并发配置
    generationConfig: {
      title: '并发配置',
      description: '限制每用户同时进行的生图任务数（含排队中和处理中）。修改后立即对新请求生效。',
      maxConcurrentPerUser: '每用户最大并发数',
      placeholder: '输入正整数',
      save: '保存配置',
      saving: '保存中...',
      saved: '配置已更新',
      saveError: '保存配置失败',
      loadError: '加载并发配置失败',
      invalidValue: '请输入有效的数字',
      mustBePositiveInteger: '必须为正整数（>= 1）',
      currentValue: '当前生效值：{value}',
      notConfigured: '未配置（使用默认值 {default}）',
      hint: '达到上限时新请求返回 409，提示用户稍后重试。设为 0 或不配置表示不限制。'
    }
  }
}
