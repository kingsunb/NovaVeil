import { useQuery } from "@tanstack/react-query";
import { api, APIError } from "@/lib/api";
import { DEFAULT_TEST_MESSAGE } from "@/lib/constants";

/** 渠道列表、编辑器和逐密钥测试共用设置中的消息及默认值。 */
export function useChannelTestMessage() {
  const { data } = useQuery({
    queryKey: ["setting", "channel_test_message"],
    queryFn: () =>
      api.getSetting("channel_test_message").catch((error: unknown) => {
        if (error instanceof APIError && error.status === 404) return null;
        throw error;
      }),
  });
  return data?.value?.trim() || DEFAULT_TEST_MESSAGE;
}
