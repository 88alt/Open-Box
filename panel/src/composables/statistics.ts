import { systemResources } from '@/composables/systemResources'
import { prettyBytesHelper } from '@/helper/utils'
import { activeConnections, downloadTotal, uploadTotal } from '@/store/connections'
import { downloadSpeed, memory, uploadSpeed } from '@/store/overview'
import { computed } from 'vue'

export enum STATISTICS_TYPE {
  CONNECTIONS = 'connections',
  ACTIVE_TERMINALS = 'statActiveTerminals',
  DOWNLOAD = 'download',
  DL_SPEED = 'dlSpeed',
  MEMORY_USAGE = 'memoryUsage',
  UPLOAD = 'upload',
  UL_SPEED = 'ulSpeed',
}

export const statisticsMap = computed(() => {
  return {
    [STATISTICS_TYPE.CONNECTIONS]: activeConnections.value.length,
    [STATISTICS_TYPE.ACTIVE_TERMINALS]: systemResources.value?.activeTerminals ?? '—',
    [STATISTICS_TYPE.MEMORY_USAGE]: prettyBytesHelper(memory.value, { binary: true }),
    [STATISTICS_TYPE.DOWNLOAD]: prettyBytesHelper(downloadTotal.value),
    [STATISTICS_TYPE.UPLOAD]: prettyBytesHelper(uploadTotal.value),
    [STATISTICS_TYPE.DL_SPEED]: prettyBytesHelper(downloadSpeed.value) + '/s',
    [STATISTICS_TYPE.UL_SPEED]: prettyBytesHelper(uploadSpeed.value) + '/s',
  }
})
